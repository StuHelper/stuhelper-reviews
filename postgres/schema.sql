CREATE SCHEMA IF NOT EXISTS reviews;
CREATE TABLE IF NOT EXISTS reviews.entries (
 id text PRIMARY KEY, root_id text NOT NULL, owner text NOT NULL, school_id text NOT NULL,
 kind text NOT NULL CHECK(kind IN ('course','mentor','reply')), target_id text NOT NULL,
 term_id text NOT NULL, state text NOT NULL CHECK(state IN ('published','pending','withdrawn','hidden','deleted')),
 created_at timestamptz NOT NULL, expires_at timestamptz, first_published_at timestamptz, data jsonb NOT NULL
);
CREATE INDEX IF NOT EXISTS reviews_chronological ON reviews.entries(kind,target_id,created_at,id);
CREATE INDEX IF NOT EXISTS reviews_owner_chronological ON reviews.entries(owner,created_at,id);
CREATE INDEX IF NOT EXISTS reviews_admin_scope ON reviews.entries(school_id,state,kind,id);
CREATE INDEX IF NOT EXISTS reviews_listing ON reviews.entries(kind,target_id,term_id,id) WHERE state='published';
CREATE INDEX IF NOT EXISTS reviews_roots ON reviews.entries(root_id,id);
CREATE INDEX IF NOT EXISTS reviews_owner ON reviews.entries(owner,id);
CREATE INDEX IF NOT EXISTS reviews_expiry ON reviews.entries(expires_at) WHERE expires_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS reviews_teachers ON reviews.entries USING gin((data->'teacher_ids')) WHERE kind='course';
CREATE TABLE IF NOT EXISTS reviews.history(id text NOT NULL REFERENCES reviews.entries(id) ON DELETE CASCADE, revision bigint NOT NULL, data jsonb NOT NULL, PRIMARY KEY(id,revision));
CREATE TABLE IF NOT EXISTS reviews.identities(root_id text NOT NULL REFERENCES reviews.entries(id) ON DELETE CASCADE, subject text NOT NULL, data jsonb NOT NULL, PRIMARY KEY(root_id,subject));
CREATE TABLE IF NOT EXISTS reviews.votes(id text NOT NULL REFERENCES reviews.entries(id) ON DELETE CASCADE, subject text NOT NULL, value integer NOT NULL CHECK(value IN(-1,1)), PRIMARY KEY(id,subject));
CREATE INDEX IF NOT EXISTS reviews_votes_owner ON reviews.votes(subject,id);
CREATE TABLE IF NOT EXISTS reviews.drafts(id text PRIMARY KEY, owner text NOT NULL, data jsonb NOT NULL);
CREATE INDEX IF NOT EXISTS reviews_drafts_owner ON reviews.drafts(owner,id);
CREATE TABLE IF NOT EXISTS reviews.replays(key text PRIMARY KEY, object_id text NOT NULL, data jsonb NOT NULL);
CREATE TABLE IF NOT EXISTS reviews.audit(id text PRIMARY KEY, data jsonb NOT NULL, reason text NOT NULL DEFAULT '', result text NOT NULL DEFAULT 'unknown' CHECK(result IN ('success','denied','failed','unknown')), recorded_xid xid8 NOT NULL DEFAULT pg_current_xact_id(), audit_actor text NOT NULL DEFAULT 'unknown', audit_target text NOT NULL DEFAULT 'unknown', audit_action text NOT NULL DEFAULT 'unknown', audit_school text NOT NULL DEFAULT '*', audit_group text NOT NULL DEFAULT '*', audit_time timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS reviews.outbox(id text PRIMARY KEY, data jsonb NOT NULL, seq bigint GENERATED ALWAYS AS IDENTITY UNIQUE, sent_at timestamptz);
CREATE INDEX IF NOT EXISTS reviews_outbox_pending ON reviews.outbox((data->>'source') COLLATE "C",(data->>'subject') COLLATE "C",seq) WHERE sent_at IS NULL;
REVOKE ALL ON SCHEMA reviews FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA reviews FROM PUBLIC;

CREATE TABLE IF NOT EXISTS reviews.presentations(school_id text PRIMARY KEY,data jsonb NOT NULL);
CREATE TABLE IF NOT EXISTS reviews.presentation_history(school_id text NOT NULL,revision bigint NOT NULL,data jsonb NOT NULL,PRIMARY KEY(school_id,revision));
REVOKE ALL ON TABLE reviews.presentations,reviews.presentation_history FROM PUBLIC;


CREATE TABLE IF NOT EXISTS reviews.closed_owners(owner text PRIMARY KEY,closure_id text NOT NULL,created_at timestamptz NOT NULL DEFAULT now());



CREATE TABLE IF NOT EXISTS reviews.audit_retention(singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton), revision bigint NOT NULL DEFAULT 0);
INSERT INTO reviews.audit_retention(singleton) VALUES(true) ON CONFLICT DO NOTHING;
CREATE OR REPLACE FUNCTION reviews.audit_project() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.audit_actor=COALESCE(NULLIF(NEW.data->>'Actor',''),'unknown'); NEW.audit_target=COALESCE(NULLIF(NEW.data->>'Subject',''),'unknown'); NEW.audit_action=NEW.data->>'Type'; NEW.audit_school=COALESCE(NULLIF(NEW.data->>'SchoolID',''),'*'); NEW.audit_time=(NEW.data->>'At')::timestamptz; RETURN NEW; END $$;
CREATE OR REPLACE TRIGGER audit_project BEFORE INSERT OR UPDATE ON reviews.audit FOR EACH ROW EXECUTE FUNCTION reviews.audit_project();
CREATE OR REPLACE FUNCTION reviews.audit_invalidate() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$ BEGIN UPDATE reviews.audit_retention SET revision=revision+1 WHERE singleton; RETURN NULL; END $$;
REVOKE ALL ON FUNCTION reviews.audit_invalidate() FROM PUBLIC;
CREATE OR REPLACE TRIGGER audit_invalidate AFTER UPDATE OR DELETE OR TRUNCATE ON reviews.audit FOR EACH STATEMENT EXECUTE FUNCTION reviews.audit_invalidate();
CREATE INDEX IF NOT EXISTS audit_query_order ON reviews.audit(audit_time DESC,(id::text) COLLATE "C" DESC);

-- Immutable source versions outlive business deletion, so rebuilds include tombstones.
CREATE TABLE IF NOT EXISTS reviews.discovery_history(id text NOT NULL, revision bigint NOT NULL CHECK(revision>0), data jsonb NOT NULL, recorded_xid xid8 NOT NULL DEFAULT pg_current_xact_id(), PRIMARY KEY(id,revision));
CREATE INDEX IF NOT EXISTS reviews_discovery_page ON reviews.discovery_history(id COLLATE "C",revision DESC);
CREATE TABLE IF NOT EXISTS reviews.discovery_retention(singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton), revision bigint NOT NULL DEFAULT 0);
INSERT INTO reviews.discovery_retention(singleton) VALUES(true) ON CONFLICT DO NOTHING;
CREATE OR REPLACE FUNCTION reviews.invalidate_discovery() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$ BEGIN UPDATE reviews.discovery_retention SET revision=revision+1 WHERE singleton; RETURN NULL; END $$;
REVOKE ALL ON FUNCTION reviews.invalidate_discovery() FROM PUBLIC;
CREATE OR REPLACE TRIGGER invalidate_discovery AFTER UPDATE OR DELETE OR TRUNCATE ON reviews.discovery_history FOR EACH STATEMENT EXECUTE FUNCTION reviews.invalidate_discovery();
REVOKE ALL ON TABLE reviews.discovery_history,reviews.discovery_retention FROM PUBLIC;
