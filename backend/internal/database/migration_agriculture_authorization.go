package database

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

func migrateAgricultureAuthorization(tx *gorm.DB) error {
	postgres := tx.Dialector.Name() == "postgres"
	epochExisted := tx.Migrator().HasTable("flow_agriculture_authorization_epochs")
	timestamp := "datetime"
	capabilityCheck := "CASE WHEN json_valid(capabilities) THEN CASE WHEN json_type(capabilities) = 'array' THEN json_array_length(capabilities) BETWEEN CASE WHEN state = 'active' THEN 1 ELSE 0 END AND 200 ELSE false END ELSE false END"
	hashCheck := "length(receipt_hash) = 64 AND trim(receipt_hash, '0123456789abcdef') = ''"
	if postgres {
		timestamp = "timestamptz"
		capabilityCheck = "CASE WHEN jsonb_typeof(capabilities::jsonb) = 'array' THEN jsonb_array_length(capabilities::jsonb) BETWEEN CASE WHEN state = 'active' THEN 1 ELSE 0 END AND 200 ELSE false END"
		hashCheck = "receipt_hash ~ '^[0-9a-f]{64}$'"
	}
	statements := []string{
		"CREATE UNIQUE INDEX IF NOT EXISTS ux_agriculture_workspace_tenant_id ON flow_workspaces(tenant_id, id)",
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS flow_agriculture_authorization_epochs (
 tenant_id varchar(40) PRIMARY KEY NOT NULL CHECK (length(tenant_id) BETWEEN 1 AND 40),
 version bigint NOT NULL CHECK (version BETWEEN 1 AND 9007199254740991),
 updated_at %s NOT NULL,
 CONSTRAINT fk_agriculture_epoch_tenant FOREIGN KEY (tenant_id) REFERENCES flow_tenants(id)
)`, timestamp),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS flow_agriculture_resource_bindings (
 id varchar(40) PRIMARY KEY NOT NULL CHECK (length(id) BETWEEN 1 AND 40),
 tenant_id varchar(40) NOT NULL CHECK (length(tenant_id) BETWEEN 1 AND 40),
 workspace_id varchar(40) NOT NULL CHECK (length(workspace_id) BETWEEN 1 AND 40),
 resource_kind varchar(24) NOT NULL CHECK (resource_kind IN ('farm','parcel','asset','job','report')),
 resource_id varchar(128) NOT NULL CHECK (length(resource_id) BETWEEN 1 AND 128),
 subject_type varchar(16) NOT NULL CHECK (subject_type = 'user'),
 subject_id varchar(36) NOT NULL CHECK (length(subject_id) BETWEEN 1 AND 36),
 capabilities text NOT NULL,
 auth_version bigint NOT NULL CHECK (auth_version BETWEEN 1 AND 9007199254740991),
 state varchar(16) NOT NULL CHECK (state IN ('active','revoked')),
 expires_at %s,
 receipt_hash varchar(64) NOT NULL CHECK (%s),
 receipt_revision bigint NOT NULL CHECK (receipt_revision BETWEEN 1 AND 9007199254740991),
 confirmed_at %s NOT NULL,
 created_at %s NOT NULL,
 updated_at %s NOT NULL,
 CONSTRAINT ck_agriculture_binding_capabilities CHECK (%s),
 CONSTRAINT ux_agriculture_resource_subject UNIQUE (tenant_id,workspace_id,resource_kind,resource_id,subject_type,subject_id),
 CONSTRAINT fk_agriculture_binding_workspace FOREIGN KEY (tenant_id,workspace_id) REFERENCES flow_workspaces(tenant_id,id),
 CONSTRAINT fk_agriculture_binding_subject FOREIGN KEY (tenant_id,subject_id) REFERENCES flow_memberships(tenant_id,user_id)
)`, timestamp, hashCheck, timestamp, timestamp, timestamp, capabilityCheck),
	}
	for _, statement := range statements {
		if err := tx.Exec(statement).Error; err != nil {
			return fmt.Errorf("migrate agriculture authorization: %w", err)
		}
	}
	// Backfill only on initial creation, before enabling FORCE RLS. Replaying the
	// migration must not disable isolation or require a hidden system scope.
	if !epochExisted {
		if err := tx.Exec(`INSERT INTO flow_agriculture_authorization_epochs(tenant_id,version,updated_at)
 SELECT DISTINCT t.id,1,CURRENT_TIMESTAMP FROM flow_tenants t
 JOIN flow_workspaces w ON w.tenant_id=t.id WHERE w.product='agriculture'
 ON CONFLICT (tenant_id) DO NOTHING`).Error; err != nil {
			return err
		}
	}
	if postgres {
		for _, table := range []string{"flow_agriculture_authorization_epochs", "flow_agriculture_resource_bindings"} {
			for _, statement := range []string{
				"ALTER TABLE " + table + " ENABLE ROW LEVEL SECURITY",
				"ALTER TABLE " + table + " FORCE ROW LEVEL SECURITY",
				"DROP POLICY IF EXISTS agriculture_tenant_isolation ON " + table,
				"CREATE POLICY agriculture_tenant_isolation ON " + table + " USING (" + agricultureTenantPolicy + ") WITH CHECK (" + agricultureTenantPolicy + ")",
			} {
				if err := tx.Exec(statement).Error; err != nil {
					return err
				}
			}
		}
		return installAgricultureEpochGuard(tx)
	}
	return nil
}

// SECURITY INVOKER preserves runtime identity and RLS. A migration owner may
// delete fixture/retired tenant state; ordinary sessions cannot reset epochs.
func installAgricultureEpochGuard(tx *gorm.DB) error {
	var schema string
	if err := tx.Raw("SELECT current_schema()").Scan(&schema).Error; err != nil || schema == "" {
		return fmt.Errorf("authorization trigger requires a current schema")
	}
	qualifiedFunction := `"` + strings.ReplaceAll(schema, `"`, `""`) + `".agriculture_guard_authorization_epoch`
	for _, statement := range []string{
		"CREATE OR REPLACE FUNCTION " + qualifiedFunction + `() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path = pg_catalog AS $epoch$
 BEGIN
   IF TG_OP = 'DELETE' THEN
     IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_class c JOIN pg_catalog.pg_roles r ON r.oid=c.relowner
                    WHERE c.oid=TG_RELID AND r.rolname=current_user) THEN
       RAISE EXCEPTION 'authorization epoch deletion is restricted to its migration owner' USING ERRCODE='42501';
     END IF;
     RETURN OLD;
   END IF;
   IF NEW.tenant_id IS DISTINCT FROM OLD.tenant_id OR NEW.version <> OLD.version + 1 THEN
     RAISE EXCEPTION 'authorization epoch must advance exactly once without changing tenant' USING ERRCODE='23514';
   END IF;
   RETURN NEW;
 END;
 $epoch$`,
		"REVOKE ALL ON FUNCTION " + qualifiedFunction + "() FROM PUBLIC",
		"DROP TRIGGER IF EXISTS agriculture_authorization_epoch_guard ON flow_agriculture_authorization_epochs",
		"CREATE TRIGGER agriculture_authorization_epoch_guard BEFORE UPDATE OR DELETE ON flow_agriculture_authorization_epochs FOR EACH ROW EXECUTE FUNCTION " + qualifiedFunction + "()",
	} {
		if err := tx.Exec(statement).Error; err != nil {
			return err
		}
	}
	return nil
}
