package edumath

import (
	"testing"

	"yingce/backend/internal/model"
)

func TestCourseConfig(t *testing.T) { runCourses(t, newHarness(t, startMathService(t))) }

func TestPGCourseConfig(t *testing.T) {
	runCourses(t, newHarnessDB(t, startMathService(t), pgTestDB(t)))
}

// runCourses (DEV-014): a workspace's course holds textbook, knowledge points,
// allowed methods, notation, language and retained revisions with optimistic
// versioning; another workspace and students cannot see it; a lesson is shown
// as aligned to the textbook only after a teacher confirms, and a change to the
// textbook or knowledge points sends confirmed alignments back to suggested.
func runCourses(t *testing.T, h *harness) {
	tenant := h.setupTeacher()
	pubID, _ := publishG1(t, h)
	var pub model.EduPublication
	h.db.First(&pub, "id = ?", pubID)
	lesson := pub.LessonRevisionID
	stu := []string{"X-Edu-Tenant", tenant}

	for _, bad := range []map[string]any{{"name": " "}, {"name": "必修", "notation": "ascii"}, {"name": "必修", "language": "fr"},
		{"name": "必修", "retainRevisions": 501}, {"name": "必修", "knowledgePoints": []string{"线面角", " "}}} {
		if c, _, _, _ := h.call("u_alice", "POST", "/api/edu/courses", bad); c != 400 {
			t.Fatalf("bad course %v must be 400, got %d", bad, c)
		}
	}
	body := map[string]any{"name": "高二数学（选择性必修一）", "grade": "高二", "textbook": "人教A版选择性必修第一册", "textbookEdition": "2019",
		"knowledgePoints": []string{"空间向量", "线面角", "二面角", "线面角"}, "allowedMethods": []string{"空间向量法", "几何法"}}
	c, out, raw, _ := h.call("u_alice", "POST", "/api/edu/courses", body)
	if c != 201 || data(out)["version"].(float64) != 1 || data(out)["notation"] != "cn_textbook" || data(out)["language"] != "zh-CN" ||
		data(out)["retainRevisions"].(float64) != 50 || len(data(out)["knowledgePoints"].([]any)) != 3 {
		t.Fatalf("create course: %d %s", c, raw)
	}
	cid := data(out)["id"].(string)
	if c, _, _, _ := h.call("u_carol", "GET", "/api/edu/courses/"+cid, nil); c != 403 && c != 404 {
		t.Fatalf("another workspace must not read the course, got %d", c)
	}
	if c, _, _, _ := h.call("u_bob", "POST", "/api/edu/courses", body, stu...); c != 403 && c != 404 {
		t.Fatalf("a student must not create courses, got %d", c)
	}
	_, out, _, _ = h.call("u_alice", "POST", "/api/edu/classes", map[string]any{"name": "高二(9)班"})
	classID := data(out)["id"].(string)
	if c, _, _, _ := h.call("u_carol", "PUT", "/api/edu/classes/"+classID+"/course", map[string]any{"courseId": cid}); c != 403 && c != 404 {
		t.Fatalf("another workspace must not set the class course, got %d", c)
	}
	if c, _, raw, _ := h.call("u_alice", "PUT", "/api/edu/classes/"+classID+"/course", map[string]any{"courseId": cid}); c != 200 {
		t.Fatalf("set class course: %d %s", c, raw)
	}

	al := "/api/edu/courses/" + cid + "/alignments"
	if c, _, _, _ := h.call("u_alice", "POST", al, map[string]any{"lessonRevisionId": lesson, "knowledgePoints": []string{"导数"}}); c != 400 {
		t.Fatalf("a point outside the course must be 400, got %d", c)
	}
	c, out, raw, _ = h.call("u_alice", "POST", al, map[string]any{"lessonRevisionId": lesson, "knowledgePoints": []string{"线面角"}, "suggestedBy": "tool:kp-suggest"})
	if c != 201 || data(out)["state"] != "suggested" || data(out)["textbookAligned"] != false {
		t.Fatalf("suggestion must not show as aligned: %d %s", c, raw)
	}
	aid := data(out)["id"].(string)
	if c, _, _, _ := h.call("u_carol", "POST", "/api/edu/course-alignments/"+aid+"/confirm", nil); c != 403 && c != 404 {
		t.Fatalf("another workspace must not confirm, got %d", c)
	}
	c, out, raw, _ = h.call("u_alice", "POST", "/api/edu/course-alignments/"+aid+"/confirm", nil)
	if c != 200 || data(out)["textbookAligned"] != true || data(out)["confirmedBy"] != "u_alice" {
		t.Fatalf("confirm: %d %s", c, raw)
	}

	up := map[string]any{}
	for k, v := range body {
		up[k] = v
	}
	up["version"], up["name"] = 7, "高二数学"
	if c, o, _, _ := h.call("u_alice", "PUT", "/api/edu/courses/"+cid, up); c != 409 || errCode(o) != "VERSION_CONFLICT" {
		t.Fatalf("stale version: %d %v", c, o)
	}
	up["version"] = 1
	c, out, raw, _ = h.call("u_alice", "PUT", "/api/edu/courses/"+cid, up)
	if c != 200 || data(out)["alignmentsNeedReconfirm"].(float64) != 0 || data(out)["course"].(map[string]any)["version"].(float64) != 2 {
		t.Fatalf("rename keeps alignment: %d %s", c, raw)
	}
	up["version"], up["textbookEdition"] = 2, "2024"
	c, out, raw, _ = h.call("u_alice", "PUT", "/api/edu/courses/"+cid, up)
	if c != 200 || data(out)["alignmentsNeedReconfirm"].(float64) != 1 {
		t.Fatalf("edition change must reopen alignment: %d %s", c, raw)
	}
	c, out, raw, _ = h.call("u_alice", "GET", "/api/edu/lesson-revisions/"+lesson+"/alignments", nil)
	a0 := out["data"].([]any)[0].(map[string]any)
	if c != 200 || a0["state"] != "suggested" || a0["textbookAligned"] != false || a0["note"] != "course_changed" {
		t.Fatalf("after course change: %d %s", c, raw)
	}
	if c, _, _, _ := h.call("u_carol", "GET", "/api/edu/lesson-revisions/"+lesson+"/alignments", nil); c != 403 && c != 404 {
		t.Fatalf("another workspace must not read alignments, got %d", c)
	}
	if c, o, _, _ := h.call("u_alice", "POST", "/api/edu/course-alignments/"+aid+"/reject", nil); c != 200 || data(o)["state"] != "rejected" {
		t.Fatalf("reject: %d %v", c, o)
	}
	c, out, raw, _ = h.call("u_alice", "GET", "/api/edu/courses/"+cid, nil)
	if c != 200 || len(data(out)["classes"].([]any)) != 1 || len(data(out)["alignments"].([]any)) != 1 {
		t.Fatalf("course detail: %d %s", c, raw)
	}
	var audits int64
	h.db.Model(&model.EduAuditEvent{}).Where("action IN ?", []string{"course.create", "course.update", "class.course", "course.align.suggested", "course.align.confirmed", "course.align.rejected"}).Count(&audits)
	if audits != 7 {
		t.Fatalf("audit trail: %d", audits)
	}
}
