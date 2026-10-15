package flow

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"yingce/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Skill is a versioned analysis method: steps, references and an output schema.
type Skill struct {
	ID           string          `json:"id"`
	Version      string          `json:"version"`
	Name         string          `json:"name"`
	Method       string          `json:"method"`
	Steps        []string        `json:"steps"`
	References   []string        `json:"references"`
	OutputSchema json.RawMessage `json:"output_schema"`
	Hash         string          `json:"hash"`
	PackID       string          `json:"pack_id"`
}

// SkillPack bundles skills; industry packs also carry source templates,
// glossary, report schema and eval samples.
type SkillPack struct {
	ID            string   `json:"id"`
	Version       string   `json:"version"`
	Name          string   `json:"name"`
	Kind          string   `json:"kind,omitempty"`
	RequiredTools []string `json:"required_tools,omitempty"`
	OptionalConns []string `json:"optional_connectors,omitempty"`
	// MCPRequirements names MCP servers the pack needs; empty means none.
	MCPRequirements []string        `json:"mcp_requirements"`
	Skills          []Skill         `json:"skills"`
	SourceTemplates json.RawMessage `json:"source_templates,omitempty"`
	Glossary        json.RawMessage `json:"glossary,omitempty"`
	ReportSchema    json.RawMessage `json:"report_schema,omitempty"`
	EvalSamples     json.RawMessage `json:"eval_samples,omitempty"`
}

// SkillRef is the frozen identity recorded on research runs and reports.
type SkillRef struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Hash    string `json:"hash"`
}

//go:embed skillpacks/*.json
var skillFS embed.FS

var (
	packsOnce sync.Once
	packs     []SkillPack
	packsErr  error
)

var ErrSkillNotFound = errors.New("flow: skill not found or not installed")

func skillHash(s Skill) string {
	s.Hash, s.PackID = "", ""
	raw, _ := json.Marshal(s)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// SkillPacks returns the embedded packs with each skill's content hash.
func SkillPacks() ([]SkillPack, error) {
	packsOnce.Do(func() {
		entries, err := skillFS.ReadDir("skillpacks")
		if err != nil {
			packsErr = err
			return
		}
		for _, e := range entries {
			raw, err := skillFS.ReadFile("skillpacks/" + e.Name())
			if err != nil {
				packsErr = err
				return
			}
			var p SkillPack
			if err := json.Unmarshal(raw, &p); err != nil {
				packsErr = err
				return
			}
			for i := range p.Skills {
				p.Skills[i].PackID = p.ID
				p.Skills[i].Hash = skillHash(p.Skills[i])
			}
			packs = append(packs, p)
		}
		sort.Slice(packs, func(i, j int) bool { return packs[i].ID < packs[j].ID })
	})
	return packs, packsErr
}

// FindSkill finds a skill by id and version ("" = latest available).
func FindSkill(id, version string) (Skill, bool) {
	ps, _ := SkillPacks()
	var best Skill
	found := false
	for _, p := range ps {
		for _, s := range p.Skills {
			if s.ID == id && (version == "" || s.Version == version) && (!found || s.Version > best.Version) {
				best, found = s, true
			}
		}
	}
	return best, found
}

// InstallSkill pins a skill version for the workspace (reinstall updates the pin).
func InstallSkill(db *gorm.DB, scope Scope, userID, id, version string) (model.FlowSkillInstall, error) {
	s, ok := FindSkill(id, version)
	if !ok {
		return model.FlowSkillInstall{}, ErrSkillNotFound
	}
	row := model.FlowSkillInstall{ID: NewID("sk_"), TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID, SkillID: s.ID,
		Version: s.Version, Hash: s.Hash, PackID: s.PackID, InstalledBy: userID, UpdatedAt: time.Now().UTC()}
	err := db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "tenant_id"}, {Name: "workspace_id"}, {Name: "skill_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"version", "hash", "pack_id", "installed_by", "updated_at"})}).Create(&row).Error
	if err != nil {
		return row, err
	}
	err = db.First(&row, "tenant_id = ? AND workspace_id = ? AND skill_id = ?", scope.TenantID, scope.WorkspaceID, s.ID).Error
	return row, err
}

func ListInstalledSkills(db *gorm.DB, scope Scope) ([]model.FlowSkillInstall, error) {
	var rows []model.FlowSkillInstall
	err := db.Where("tenant_id = ? AND workspace_id = ?", scope.TenantID, scope.WorkspaceID).Order("skill_id").Find(&rows).Error
	return rows, err
}

// LoadSkillForRun returns the installed skill, frozen for runID: the first load
// pins version+hash; later loads in the same run get the pinned version even if
// the workspace installs a different version meanwhile.
func LoadSkillForRun(db *gorm.DB, scope Scope, runID, id string) (Skill, error) {
	if runID != "" {
		var pin model.FlowSkillRunPin
		if err := db.First(&pin, "run_id = ? AND skill_id = ? AND tenant_id = ?", runID, id, scope.TenantID).Error; err == nil {
			s, ok := FindSkill(id, pin.Version)
			if !ok || s.Hash != pin.Hash {
				return Skill{}, errors.New("flow: pinned skill version is no longer available")
			}
			return s, nil
		}
	}
	var inst model.FlowSkillInstall
	if err := db.First(&inst, "tenant_id = ? AND workspace_id = ? AND skill_id = ?", scope.TenantID, scope.WorkspaceID, id).Error; err != nil {
		return Skill{}, ErrSkillNotFound
	}
	s, ok := FindSkill(id, inst.Version)
	if !ok || s.Hash != inst.Hash {
		return Skill{}, ErrSkillNotFound
	}
	if runID != "" {
		db.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.FlowSkillRunPin{RunID: runID, SkillID: id, TenantID: scope.TenantID, Version: s.Version, Hash: s.Hash, CreatedAt: time.Now().UTC()})
		return LoadSkillForRun(db, scope, runID, id)
	}
	return s, nil
}

func (s Skill) Ref() SkillRef { return SkillRef{ID: s.ID, Version: s.Version, Hash: s.Hash} }
