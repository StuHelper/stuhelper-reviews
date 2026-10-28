CREATE SCHEMA IF NOT EXISTS reviews;
CREATE TABLE IF NOT EXISTS reviews.entries (
 id text PRIMARY KEY, root_id text NOT NULL, owner text NOT NULL, school_id text NOT NULL,
 kind text NOT NULL CHECK(kind IN ('course','mentor','reply')), target_id text NOT NULL,
 term_id text NOT NULL, state text NOT NULL CHECK(state IN ('published','pending','withdrawn','hidden','deleted')),
 expires_at timestamptz, first_published_at timestamptz, data jsonb NOT NULL
);
-- Backfill the original creation time; editing and republishing do not reorder a discussion.
ALTER TABLE reviews.entries ADD COLUMN IF NOT EXISTS created_at timestamptz;
UPDATE reviews.entries SET created_at=(data->>'created_at')::timestamptz WHERE created_at IS NULL;
ALTER TABLE reviews.entries ALTER COLUMN created_at SET NOT NULL;
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
CREATE TABLE IF NOT EXISTS reviews.bookmarks(subject text NOT NULL, target_id text NOT NULL, PRIMARY KEY(subject,target_id));
CREATE TABLE IF NOT EXISTS reviews.drafts(id text PRIMARY KEY, owner text NOT NULL, data jsonb NOT NULL);
CREATE INDEX IF NOT EXISTS reviews_drafts_owner ON reviews.drafts(owner,id);
CREATE TABLE IF NOT EXISTS reviews.replays(key text PRIMARY KEY, object_id text NOT NULL, data jsonb NOT NULL);
CREATE TABLE IF NOT EXISTS reviews.audit(id text PRIMARY KEY, data jsonb NOT NULL);
CREATE TABLE IF NOT EXISTS reviews.outbox(id text PRIMARY KEY, data jsonb NOT NULL, delivered boolean NOT NULL DEFAULT false);
ALTER TABLE reviews.outbox ADD COLUMN IF NOT EXISTS attempts integer NOT NULL DEFAULT 0;
ALTER TABLE reviews.outbox ADD COLUMN IF NOT EXISTS next_attempt timestamptz NOT NULL DEFAULT '-infinity';
ALTER TABLE reviews.outbox ADD COLUMN IF NOT EXISTS first_failure timestamptz;
ALTER TABLE reviews.outbox ADD COLUMN IF NOT EXISTS blocked boolean NOT NULL DEFAULT false;
CREATE INDEX IF NOT EXISTS reviews_outbox_ready ON reviews.outbox(next_attempt,id) WHERE NOT delivered AND NOT blocked;
REVOKE ALL ON SCHEMA reviews FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA reviews FROM PUBLIC;
