package database

import (
	"gorm.io/gorm"
	"yingce/backend/internal/model"
)

func migrateAgricultureFarms(tx *gorm.DB) error {
	if err := tx.AutoMigrate(&model.AgricultureFarmOperation{}, &model.AgricultureFarmEvent{}); err != nil {
		return err
	}
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	for _, table := range []string{"flow_agriculture_farm_operations", "flow_agriculture_farm_events"} {
		for _, sql := range []string{
			"ALTER TABLE " + table + " ENABLE ROW LEVEL SECURITY",
			"ALTER TABLE " + table + " FORCE ROW LEVEL SECURITY",
			"CREATE POLICY agriculture_tenant_isolation ON " + table + " USING (" + agricultureTenantPolicy + ") WITH CHECK (" + agricultureTenantPolicy + ")",
			"REVOKE ALL ON " + table + " FROM PUBLIC",
			"GRANT SELECT,INSERT,UPDATE,DELETE ON " + table + " TO flow_platform_app",
		} {
			if err := tx.Exec(sql).Error; err != nil {
				return err
			}
		}
	}
	for _, sql := range []string{
		`ALTER TABLE flow_agriculture_farm_operations ADD CONSTRAINT agriculture_farm_operation_values CHECK (action IN ('create','update','archive') AND state IN ('preview','pending','completed','rejected','reconciliation_required') AND expected_revision >= 0 AND result_revision >= 0 AND request_hash ~ '^[0-9a-f]{64}$' AND grant_plan_hash ~ '^[0-9a-f]{64}$')`,
		`ALTER TABLE flow_agriculture_farm_events ADD CONSTRAINT agriculture_farm_event_values CHECK (producer='agri-gis' AND revision>0 AND payload_hash ~ '^[0-9a-f]{64}$')`,
		`REVOKE UPDATE,DELETE ON flow_agriculture_farm_events FROM flow_platform_app`,
		`CREATE FUNCTION agriculture_farm_intent_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
		 IF TG_OP='DELETE' THEN
		  IF OLD.state<>'preview' OR OLD.command_id<>'' THEN RAISE EXCEPTION 'farm operation is immutable'; END IF;
		  RETURN OLD;
		 END IF;
		 IF ROW(NEW.id,NEW.tenant_id,NEW.workspace_id,NEW.actor_id,NEW.action,NEW.expected_revision,NEW.request_hash,NEW.grant_plan_hash,NEW.payload,NEW.expires_at,NEW.created_at) IS DISTINCT FROM ROW(OLD.id,OLD.tenant_id,OLD.workspace_id,OLD.actor_id,OLD.action,OLD.expected_revision,OLD.request_hash,OLD.grant_plan_hash,OLD.payload,OLD.expires_at,OLD.created_at) THEN RAISE EXCEPTION 'farm intent binding is immutable'; END IF;
		 IF OLD.farm_id<>'' AND NEW.farm_id<>OLD.farm_id THEN RAISE EXCEPTION 'farm identity is immutable'; END IF;
		 IF OLD.command_id<>'' AND NEW.command_id<>OLD.command_id THEN RAISE EXCEPTION 'farm command binding is immutable'; END IF;
		 IF OLD.idempotency_hash IS NOT NULL AND NEW.idempotency_hash IS DISTINCT FROM OLD.idempotency_hash THEN RAISE EXCEPTION 'farm idempotency binding is immutable'; END IF;
		 IF OLD.state IN ('completed','rejected','reconciliation_required') AND NEW IS DISTINCT FROM OLD THEN RAISE EXCEPTION 'farm completion is immutable'; END IF;
		 IF NEW.state<>OLD.state AND NOT ((OLD.state='preview' AND NEW.state='pending') OR (OLD.state='pending' AND NEW.state IN ('completed','rejected','reconciliation_required'))) THEN RAISE EXCEPTION 'invalid farm operation transition'; END IF;
		 RETURN NEW;
		END $$`,
		`CREATE TRIGGER agriculture_farm_intent_immutable BEFORE UPDATE OR DELETE ON flow_agriculture_farm_operations FOR EACH ROW EXECUTE FUNCTION agriculture_farm_intent_guard()`,
	} {
		if err := tx.Exec(sql).Error; err != nil {
			return err
		}
	}
	return nil
}
