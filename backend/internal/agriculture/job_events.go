package agriculture

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"yingce/backend/internal/agricanonical"
	"yingce/backend/internal/kernel"
)

// JobEventProvider is a trusted, typed domain adapter, NOT caller JSON or a
// generic queue. It must query its own scope/ownership and durable replay window.
// Generation changes on stream replacement/trim invalidation, NOT every append.
type JobStreamBinding struct {
	Scope                         ProjectionScope
	JobID, Projection, Generation string
}
type JobEvent struct {
	Sequence int64           `json:"sequence"`
	Data     json.RawMessage `json:"data"`
}
type JobEventProvider interface {
	Describe(context.Context, ProjectionScope, string) (JobStreamBinding, error)
	Read(context.Context, JobStreamBinding, int64, int) ([]JobEvent, error)
}
type JobEventStream struct {
	provider       JobEventProvider
	key            []byte
	now            func() time.Time
	poll, duration time.Duration
	slots          chan struct{}
}

func NewJobEventStream(provider JobEventProvider, key []byte) (*JobEventStream, error) {
	if provider == nil || len(key) != 32 {
		return nil, ErrDependencyUnavailable
	}
	return &JobEventStream{provider: provider, key: slices.Clone(key), now: time.Now, poll: time.Second, duration: 30 * time.Second, slots: make(chan struct{}, 32)}, nil
}

type eventCursor struct {
	Binding  string `json:"binding"`
	Sequence int64  `json:"sequence"`
	Expires  int64  `json:"expires"`
}

func (s *JobEventStream) cursor(binding string, sequence int64) (string, error) {
	raw, e := json.Marshal(eventCursor{binding, sequence, s.now().Add(5 * time.Minute).Unix()})
	if e != nil {
		return "", e
	}
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte("agriculture/job-events/cursor/v1\x00"))
	mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}
func (s *JobEventStream) resume(raw, binding string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	if len(raw) > 512 {
		return 0, ErrInvalid
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 2 {
		return 0, ErrInvalid
	}
	b, e := decodeIdentitySegment(parts[0])
	if e != nil {
		return 0, ErrInvalid
	}
	sig, e := decodeIdentitySegment(parts[1])
	if e != nil || len(sig) != 32 {
		return 0, ErrInvalid
	}
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte("agriculture/job-events/cursor/v1\x00"))
	mac.Write(b)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return 0, ErrForbidden
	}
	var c eventCursor
	if strictIdentityJSON(b, &c, 384) != nil || c.Sequence < 0 || c.Sequence > maximumAuthorizationVersion {
		return 0, ErrInvalid
	}
	if c.Binding != binding || c.Expires <= s.now().Unix() || c.Expires > s.now().Add(5*time.Minute+30*time.Second).Unix() {
		return 0, ErrConflict
	}
	return c.Sequence, nil
}
func streamDigest(d AuthorizationDecision, b JobStreamBinding) (string, error) {
	if !scopeEqual(b.Scope, projectionScope(d)) || !ownershipID.MatchString(b.JobID) || !ownershipID.MatchString(b.Projection) || !ownershipID.MatchString(b.Generation) {
		return "", ErrDependencyUnavailable
	}
	return projectionDigest(d, ProjectionBinding{b.Scope, b.Projection, b.Generation}, []byte(`{"filter":"all","representation":"job-events-v1"}`))
}

// RegisterJobEvents is called only by platform composition. No configured
// provider means a registered, authenticated 503 boundary, not mock events.
func RegisterJobEvents(api gin.IRouter, a API, s *JobEventStream) {
	group := api.Group("/agri/v1/workspaces/:workspace_id/jobs/:job_id/events")
	group.Use(a.sessionMiddleware(respond))
	group.GET("", func(c *gin.Context) {
		if s == nil || s.provider == nil {
			respond(c, nil, ErrDependencyUnavailable)
			return
		}
		if c.Request.URL.RawQuery != "" || c.Request.URL.ForceQuery || c.Request.URL.RawPath != "" || len(c.Request.Header.Values("Last-Event-ID")) > 1 || c.Request.ContentLength > 0 || len(c.Request.TransferEncoding) > 0 {
			respond(c, nil, ErrInvalid)
			return
		}
		job, workspace := c.Param("job_id"), c.Param("workspace_id")
		if !ownershipID.MatchString(job) || !ownershipID.MatchString(workspace) {
			respond(c, nil, ErrInvalid)
			return
		}
		select {
		case s.slots <- struct{}{}:
			defer func() { <-s.slots }()
		default:
			respond(c, nil, ErrDependencyUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), s.duration)
		defer cancel()
		refresh := func(ctx context.Context) (string, error) {
			u, e := a.User(c)
			if e != nil {
				var appErr *kernel.AppError
				if errors.As(e, &appErr) && (appErr.Status == 401 || appErr.Status == 403) {
					return "", ErrUnauthenticated
				}
				return "", e
			}
			if u == nil {
				return "", ErrUnauthenticated
			}
			return u.ID, nil
		}
		q := ProjectionQuery{Action: "agri.analysis.read", Resources: []ResourceRef{{Kind: "job", ID: job}}}
		resolve := func() (AuthorizationDecision, JobStreamBinding, string, error) {
			d, e := freshProjection(ctx, a.Service, refresh, workspace, q)
			if e != nil {
				return d, JobStreamBinding{}, "", e
			}
			b, e := s.provider.Describe(ctx, projectionScope(d), job)
			if e != nil {
				return d, b, "", e
			}
			if b.JobID != job {
				return d, b, "", ErrDependencyUnavailable
			}
			// Domain resolution may wait: session and authority are checked again
			// after that wait before any buffered event becomes visible.
			actor, e := refresh(ctx)
			if e != nil {
				return d, b, "", e
			}
			d, e = a.Service.Reauthorize(ctx, actor, d)
			if e != nil {
				return d, b, "", e
			}
			digest, e := streamDigest(d, b)
			return d, b, digest, e
		}
		_, binding, digest, e := resolve()
		if e != nil {
			respond(c, nil, e)
			return
		}
		after, e := s.resume(c.GetHeader("Last-Event-ID"), digest)
		if e != nil {
			respond(c, nil, e)
			return
		}
		started := false
		fail := func(e error) {
			if !started {
				respond(c, nil, e)
			}
		}
		for {
			if ctx.Err() != nil {
				return
			}
			events, e := s.provider.Read(ctx, binding, after, 32)
			if e != nil {
				fail(e)
				return
			}
			if len(events) > 32 {
				fail(ErrDependencyUnavailable)
				return
			}
			// Validate the entire batch before exposing even its first sensitive byte.
			sequence := after
			for _, event := range events {
				if event.Sequence <= sequence || event.Sequence > maximumAuthorizationVersion || len(event.Data) > 16<<10 {
					fail(ErrDependencyUnavailable)
					return
				}
				if _, e := agricanonical.Canonicalize(event.Data); e != nil {
					fail(ErrDependencyUnavailable)
					return
				}
				sequence = event.Sequence
			}
			_, next, current, e := resolve()
			if e != nil {
				fail(e)
				return
			}
			if current != digest {
				fail(ErrConflict)
				return
			}
			binding = next
			if ctx.Err() != nil {
				return
			}
			controller := http.NewResponseController(c.Writer)
			// No long database transaction is held while writing or waiting. A slow
			// browser has a bounded write deadline, not an unbounded worker lease.
			if e = controller.SetWriteDeadline(time.Now().Add(2 * time.Second)); e != nil {
				fail(ErrDependencyUnavailable)
				return
			}
			if !started {
				c.Header("Content-Type", "text/event-stream")
				c.Header("Cache-Control", "no-store")
				c.Header("X-Accel-Buffering", "no")
				c.Header("X-Content-Type-Options", "nosniff")
				c.Status(200)
				started = true
			}
			for _, event := range events {
				cursor, e := s.cursor(digest, event.Sequence)
				if e != nil {
					return
				}
				raw, e := json.Marshal(event)
				if e != nil {
					return
				}
				if _, e = fmt.Fprintf(c.Writer, "id: %s\nevent: job\ndata: %s\n\n", cursor, raw); e != nil {
					return
				}
				after = event.Sequence
			}
			if len(events) == 0 {
				if _, e = fmt.Fprint(c.Writer, ": keepalive\n\n"); e != nil {
					return
				}
			}
			if e = controller.Flush(); e != nil {
				return
			}
			timer := time.NewTimer(s.poll)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	})
}
