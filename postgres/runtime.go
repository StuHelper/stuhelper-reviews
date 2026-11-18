package postgres

import (
	"context"
	reviews "github.com/StuHelper/stuhelper-reviews"
)

// CheckRuntime rejects migration identities and privileges crossing module schemas.
func (r *Repository) CheckRuntime(ctx context.Context) error {
	var unsafe bool
	err := r.DB.QueryRowContext(ctx, `SELECT rolsuper OR rolcreaterole OR rolcreatedb OR rolbypassrls
 OR has_schema_privilege(current_user,'reviews','CREATE')
 OR has_database_privilege(current_user,current_database(),'CREATE')
 OR pg_has_role(current_user,'pg_read_all_data','MEMBER')
 OR pg_has_role(current_user,'pg_write_all_data','MEMBER')
 OR EXISTS(SELECT 1 FROM pg_namespace WHERE nspname='reviews' AND nspowner=(SELECT oid FROM pg_roles WHERE rolname=current_user))
 OR EXISTS (
  SELECT 1 FROM (VALUES ('reviews.audit'), ('reviews.discovery_history'), ('reviews.activity_history')) AS protected(relation)
  WHERE has_table_privilege(current_user, relation, 'DELETE,TRUNCATE')
     OR has_any_column_privilege(current_user, relation, 'UPDATE')
 )
 OR EXISTS(SELECT 1 FROM pg_namespace WHERE nspname NOT IN ('reviews','public','information_schema') AND nspname NOT LIKE 'pg_%' AND has_schema_privilege(current_user,oid,'USAGE'))
 FROM pg_roles WHERE rolname=current_user`).Scan(&unsafe)
	if err != nil || unsafe {
		return reviews.ErrUnavailable
	}
	return nil
}
