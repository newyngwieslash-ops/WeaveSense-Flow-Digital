package database

import (
	"gorm.io/gorm"
	"yingce/backend/internal/model"
)

func migrateAgricultureParcels(tx *gorm.DB) error {
	if e := tx.AutoMigrate(&model.AgricultureParcelOperation{}, &model.AgricultureParcelEvent{}, &model.AgricultureParcelReference{}); e != nil {
		return e
	}
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	for _, table := range []string{"flow_agriculture_parcel_operations", "flow_agriculture_parcel_events", "flow_agriculture_parcel_references"} {
		for _, sql := range []string{"ALTER TABLE " + table + " ENABLE ROW LEVEL SECURITY", "ALTER TABLE " + table + " FORCE ROW LEVEL SECURITY", "CREATE POLICY agriculture_tenant_isolation ON " + table + " USING (" + agricultureTenantPolicy + ") WITH CHECK (" + agricultureTenantPolicy + ")", "REVOKE ALL ON " + table + " FROM PUBLIC", "GRANT SELECT,INSERT,UPDATE,DELETE ON " + table + " TO flow_platform_app"} {
			if e := tx.Exec(sql).Error; e != nil {
				return e
			}
		}
	}
	for _, sql := range []string{
		`ALTER TABLE flow_agriculture_parcel_operations ADD CONSTRAINT agriculture_parcel_operation_values CHECK (action IN ('create','revise') AND state IN ('preview','pending','completed','rejected','reconciliation_required') AND expected_revision>=0 AND expected_farm_revision>=0 AND result_revision>=0 AND request_hash ~ '^[0-9a-f]{64}$')`,
		`ALTER TABLE flow_agriculture_parcel_events ADD CONSTRAINT agriculture_parcel_event_values CHECK (producer='agri-gis' AND geometry_revision>0 AND payload_hash ~ '^[0-9a-f]{64}$')`,
		`REVOKE UPDATE,DELETE ON flow_agriculture_parcel_events,flow_agriculture_parcel_references FROM flow_platform_app`,
		`CREATE FUNCTION agriculture_parcel_intent_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' THEN IF OLD.state<>'preview' OR OLD.command_id<>'' THEN RAISE EXCEPTION 'parcel operation is immutable'; END IF; RETURN OLD; END IF;
 IF ROW(NEW.id,NEW.tenant_id,NEW.workspace_id,NEW.actor_id,NEW.action,NEW.farm_id,NEW.expected_revision,NEW.expected_farm_revision,NEW.request_hash,NEW.payload,NEW.expires_at,NEW.created_at) IS DISTINCT FROM ROW(OLD.id,OLD.tenant_id,OLD.workspace_id,OLD.actor_id,OLD.action,OLD.farm_id,OLD.expected_revision,OLD.expected_farm_revision,OLD.request_hash,OLD.payload,OLD.expires_at,OLD.created_at) THEN RAISE EXCEPTION 'parcel intent binding is immutable'; END IF;
 IF OLD.parcel_id<>'' AND NEW.parcel_id<>OLD.parcel_id THEN RAISE EXCEPTION 'parcel identity is immutable'; END IF;
 IF OLD.command_id<>'' AND NEW.command_id<>OLD.command_id THEN RAISE EXCEPTION 'parcel command binding is immutable'; END IF;
 IF OLD.idempotency_hash IS NOT NULL AND NEW.idempotency_hash IS DISTINCT FROM OLD.idempotency_hash THEN RAISE EXCEPTION 'parcel idempotency binding is immutable'; END IF;
 IF OLD.state IN ('completed','rejected','reconciliation_required') AND NEW IS DISTINCT FROM OLD THEN RAISE EXCEPTION 'parcel completion is immutable'; END IF;
 IF NEW.state<>OLD.state AND NOT ((OLD.state='preview' AND NEW.state='pending') OR (OLD.state='pending' AND NEW.state IN ('completed','rejected','reconciliation_required'))) THEN RAISE EXCEPTION 'invalid parcel operation transition'; END IF;
 RETURN NEW; END $$`,
		`CREATE TRIGGER agriculture_parcel_intent_immutable BEFORE UPDATE OR DELETE ON flow_agriculture_parcel_operations FOR EACH ROW EXECUTE FUNCTION agriculture_parcel_intent_guard()`,
	} {
		if e := tx.Exec(sql).Error; e != nil {
			return e
		}
	}
	return nil
}
