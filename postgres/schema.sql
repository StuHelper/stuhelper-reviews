CREATE SCHEMA IF NOT EXISTS reviews;
CREATE TABLE IF NOT EXISTS reviews.entries (
 id text PRIMARY KEY, root_id text NOT NULL, owner text NOT NULL, school_id text NOT NULL,
 kind text NOT NULL CHECK(kind IN ('course','mentor','reply')), target_id text NOT NULL,
 term_id text NOT NULL, state text NOT NULL CHECK(state IN ('published','pending','withdrawn','hidden','deleted')),
 upvotes integer NOT NULL DEFAULT 0 CHECK(upvotes>=0),
 created_at timestamptz NOT NULL, expires_at timestamptz, first_published_at timestamptz, data jsonb NOT NULL
);
DO $$ BEGIN
 IF NOT EXISTS (SELECT FROM pg_attribute WHERE attrelid='reviews.entries'::regclass AND attname='upvotes' AND NOT attisdropped) THEN
  ALTER TABLE reviews.entries ADD COLUMN upvotes integer NOT NULL DEFAULT 0 CHECK(upvotes>=0);
  UPDATE reviews.entries e SET upvotes=v.total FROM
   (SELECT id,count(*) AS total FROM reviews.votes WHERE value=1 GROUP BY id) v WHERE e.id=v.id;
 END IF;
END $$;
CREATE INDEX IF NOT EXISTS reviews_chronological ON reviews.entries(kind,target_id,created_at,id);
CREATE INDEX IF NOT EXISTS reviews_owner_chronological ON reviews.entries(owner,created_at,id);
CREATE INDEX IF NOT EXISTS reviews_admin_scope ON reviews.entries(school_id,state,kind,id);
CREATE INDEX IF NOT EXISTS reviews_admin_chronological ON reviews.entries(school_id,created_at DESC,id DESC) WHERE state<>'withdrawn';
CREATE INDEX IF NOT EXISTS reviews_admin_review_requested ON reviews.entries(school_id,created_at DESC,id DESC) WHERE state IN ('pending','published') AND COALESCE((data->>'review_requested')::boolean,false);
CREATE INDEX IF NOT EXISTS reviews_listing ON reviews.entries(kind,target_id,term_id,id) WHERE state='published';
CREATE INDEX IF NOT EXISTS reviews_upvotes_school ON reviews.entries(school_id,upvotes DESC,created_at DESC,id DESC) WHERE state='published';
CREATE INDEX IF NOT EXISTS reviews_upvotes_target ON reviews.entries(kind,target_id,upvotes DESC,created_at DESC,id DESC) WHERE state='published';
CREATE INDEX IF NOT EXISTS reviews_roots ON reviews.entries(root_id,id);
CREATE INDEX IF NOT EXISTS reviews_owner ON reviews.entries(owner,id);
CREATE INDEX IF NOT EXISTS reviews_expiry ON reviews.entries(expires_at) WHERE expires_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS reviews_teachers ON reviews.entries USING gin((data->'teacher_ids')) WHERE kind='course';
CREATE INDEX IF NOT EXISTS reviews_teacher_replies ON reviews.entries(root_id,id) WHERE COALESCE((data->>'teacher_reply')::boolean,false);
CREATE TABLE IF NOT EXISTS reviews.history(id text NOT NULL REFERENCES reviews.entries(id) ON DELETE CASCADE, revision bigint NOT NULL, data jsonb NOT NULL, PRIMARY KEY(id,revision));
CREATE TABLE IF NOT EXISTS reviews.identities(root_id text NOT NULL REFERENCES reviews.entries(id) ON DELETE CASCADE, subject text NOT NULL, data jsonb NOT NULL, PRIMARY KEY(root_id,subject));
CREATE TABLE IF NOT EXISTS reviews.votes(id text NOT NULL REFERENCES reviews.entries(id) ON DELETE CASCADE, subject text NOT NULL, value integer NOT NULL CHECK(value IN(-1,1)), created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(id,subject));
CREATE INDEX IF NOT EXISTS reviews_votes_owner ON reviews.votes(subject,id);
CREATE OR REPLACE FUNCTION reviews.sync_entry_upvotes() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE delta integer := 0; target text;
BEGIN
 IF TG_OP = 'INSERT' THEN
  target := NEW.id;
  IF NEW.value = 1 THEN delta := 1; END IF;
 ELSIF TG_OP = 'DELETE' THEN
  target := OLD.id;
  IF OLD.value = 1 THEN delta := -1; END IF;
 ELSE
  target := NEW.id;
  IF OLD.value = 1 AND NEW.value <> 1 THEN delta := -1;
  ELSIF OLD.value <> 1 AND NEW.value = 1 THEN delta := 1;
  END IF;
 END IF;
 IF delta <> 0 THEN
  UPDATE reviews.entries SET upvotes = GREATEST(0, upvotes + delta) WHERE id = target;
 END IF;
 RETURN NULL;
END $$;
CREATE OR REPLACE TRIGGER sync_entry_upvotes AFTER INSERT OR UPDATE OR DELETE ON reviews.votes FOR EACH ROW EXECUTE FUNCTION reviews.sync_entry_upvotes();
CREATE TABLE IF NOT EXISTS reviews.drafts(id text PRIMARY KEY, owner text NOT NULL, data jsonb NOT NULL);
CREATE INDEX IF NOT EXISTS reviews_drafts_owner ON reviews.drafts(owner,id);
CREATE TABLE IF NOT EXISTS reviews.replays(key text PRIMARY KEY, object_id text NOT NULL, owner text NOT NULL DEFAULT '', expires_at timestamptz, data jsonb NOT NULL);
ALTER TABLE reviews.replays ADD COLUMN IF NOT EXISTS owner text NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS reviews_replays_owner ON reviews.replays(owner) WHERE owner<>'';
DO $$ BEGIN
 IF NOT EXISTS (SELECT FROM pg_attribute WHERE attrelid='reviews.replays'::regclass AND attname='expires_at' AND NOT attisdropped) THEN
  ALTER TABLE reviews.replays ADD COLUMN expires_at timestamptz;
  UPDATE reviews.replays SET expires_at=(data->>'Until')::timestamptz
   WHERE data->>'Until' IS NOT NULL AND data->>'Until'<>'0001-01-01T00:00:00Z';
 END IF;
END $$;
CREATE INDEX IF NOT EXISTS reviews_replays_expiry ON reviews.replays(expires_at,key) WHERE expires_at IS NOT NULL;
CREATE TABLE IF NOT EXISTS reviews.audit(id text PRIMARY KEY, data jsonb NOT NULL, reason text NOT NULL DEFAULT '', result text NOT NULL DEFAULT 'unknown' CHECK(result IN ('success','denied','failed','unknown')), recorded_xid xid8 NOT NULL DEFAULT pg_current_xact_id(), audit_actor text NOT NULL DEFAULT 'unknown', audit_target text NOT NULL DEFAULT 'unknown', audit_action text NOT NULL DEFAULT 'unknown', audit_school text NOT NULL DEFAULT '*', audit_group text NOT NULL DEFAULT '*', audit_time timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS reviews.outbox(id text PRIMARY KEY, data jsonb NOT NULL, seq bigint GENERATED ALWAYS AS IDENTITY UNIQUE, sent_at timestamptz);
CREATE INDEX IF NOT EXISTS reviews_outbox_pending ON reviews.outbox((data->>'source') COLLATE "C",(data->>'subject') COLLATE "C",seq) WHERE sent_at IS NULL;
CREATE INDEX IF NOT EXISTS reviews_outbox_sent ON reviews.outbox(sent_at,id) WHERE sent_at IS NOT NULL;
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
DROP TRIGGER IF EXISTS audit_invalidate ON reviews.audit;
CREATE TRIGGER audit_invalidate AFTER UPDATE OR DELETE ON reviews.audit FOR EACH ROW EXECUTE FUNCTION reviews.audit_invalidate();
CREATE OR REPLACE TRIGGER truncate_audit AFTER TRUNCATE ON reviews.audit FOR EACH STATEMENT EXECUTE FUNCTION reviews.audit_invalidate();
CREATE INDEX IF NOT EXISTS audit_query_order ON reviews.audit(audit_time DESC,(id::text) COLLATE "C" DESC);
CREATE INDEX IF NOT EXISTS audit_actor_order ON reviews.audit(audit_actor,audit_time DESC,(id::text) COLLATE "C" DESC);
CREATE INDEX IF NOT EXISTS audit_target_order ON reviews.audit(audit_target,audit_time DESC,(id::text) COLLATE "C" DESC);
CREATE INDEX IF NOT EXISTS audit_actor_type ON reviews.audit((data->>'Actor'),(data->>'Type')) WHERE (data->>'Actor') IS NOT NULL AND (data->>'Actor')<>'';

-- Immutable source versions outlive business deletion, so rebuilds include tombstones.
CREATE TABLE IF NOT EXISTS reviews.discovery_history(id text NOT NULL, revision bigint NOT NULL CHECK(revision>0), data jsonb NOT NULL, recorded_xid xid8 NOT NULL DEFAULT pg_current_xact_id(), PRIMARY KEY(id,revision));
CREATE INDEX IF NOT EXISTS reviews_discovery_page ON reviews.discovery_history(id COLLATE "C",revision DESC);
CREATE TABLE IF NOT EXISTS reviews.discovery_retention(singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton), revision bigint NOT NULL DEFAULT 0);
INSERT INTO reviews.discovery_retention(singleton) VALUES(true) ON CONFLICT DO NOTHING;
CREATE OR REPLACE FUNCTION reviews.invalidate_discovery() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$ BEGIN UPDATE reviews.discovery_retention SET revision=revision+1 WHERE singleton; RETURN NULL; END $$;
REVOKE ALL ON FUNCTION reviews.invalidate_discovery() FROM PUBLIC;
DROP TRIGGER IF EXISTS invalidate_discovery ON reviews.discovery_history;
CREATE TRIGGER invalidate_discovery AFTER UPDATE OR DELETE ON reviews.discovery_history FOR EACH ROW EXECUTE FUNCTION reviews.invalidate_discovery();
CREATE OR REPLACE TRIGGER truncate_discovery AFTER TRUNCATE ON reviews.discovery_history FOR EACH STATEMENT EXECUTE FUNCTION reviews.invalidate_discovery();
REVOKE ALL ON TABLE reviews.discovery_history,reviews.discovery_retention FROM PUBLIC;

-- Provider-owned daily aggregates and immutable versions used by rebuild cursors.
CREATE TABLE IF NOT EXISTS reviews.activity_buckets(id text PRIMARY KEY,kind text NOT NULL CHECK(kind IN('course','teacher')),target_id text NOT NULL,school_id text NOT NULL,day date NOT NULL,first_count bigint NOT NULL,second_count bigint NOT NULL,revision bigint NOT NULL CHECK(revision>0),visible boolean NOT NULL,data jsonb NOT NULL DEFAULT '{}',UNIQUE(kind,target_id,day));
CREATE INDEX IF NOT EXISTS reviews_activity_target ON reviews.activity_buckets(kind,target_id,day);
CREATE TABLE IF NOT EXISTS reviews.activity_history(id text NOT NULL,revision bigint NOT NULL,day date NOT NULL,data jsonb NOT NULL,recorded_xid xid8 NOT NULL DEFAULT pg_current_xact_id(),PRIMARY KEY(id,revision));
CREATE INDEX IF NOT EXISTS reviews_activity_history_page ON reviews.activity_history(id COLLATE "C",revision DESC);
CREATE INDEX IF NOT EXISTS reviews_activity_history_day ON reviews.activity_history(day,id,revision);
CREATE TABLE IF NOT EXISTS reviews.activity_retention(singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),revision bigint NOT NULL DEFAULT 0);
INSERT INTO reviews.activity_retention(singleton) VALUES(true) ON CONFLICT DO NOTHING;
CREATE OR REPLACE FUNCTION reviews.invalidate_activity() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$ BEGIN UPDATE reviews.activity_retention SET revision=revision+1 WHERE singleton;RETURN NULL;END $$;
REVOKE ALL ON FUNCTION reviews.invalidate_activity() FROM PUBLIC;
CREATE OR REPLACE TRIGGER invalidate_activity AFTER UPDATE OR DELETE ON reviews.activity_history FOR EACH ROW EXECUTE FUNCTION reviews.invalidate_activity();
CREATE OR REPLACE TRIGGER truncate_activity AFTER TRUNCATE ON reviews.activity_history FOR EACH STATEMENT EXECUTE FUNCTION reviews.invalidate_activity();
REVOKE ALL ON TABLE reviews.activity_buckets,reviews.activity_history,reviews.activity_retention FROM PUBLIC;

CREATE TABLE IF NOT EXISTS reviews.activity_contributions(root_id text NOT NULL,id text NOT NULL,kind text NOT NULL,target_id text NOT NULL,school_id text NOT NULL,day date NOT NULL,reviews bigint NOT NULL,interactions bigint NOT NULL,PRIMARY KEY(root_id,id));
REVOKE ALL ON reviews.activity_contributions FROM PUBLIC;

CREATE INDEX IF NOT EXISTS reviews_vote_activity_day ON reviews.votes(id,created_at);

-- Attribute legacy receipts before removing their only audit-to-account link.
UPDATE reviews.replays r SET owner=a.data->>'Actor'
 FROM reviews.audit a WHERE a.id=r.key AND r.owner='' AND COALESCE(a.data->>'Actor','') NOT IN ('','anonymous');
UPDATE reviews.replays r SET owner=d.owner FROM reviews.drafts d WHERE r.owner='' AND r.object_id=d.id;
DELETE FROM reviews.replays r USING reviews.closed_owners c WHERE r.owner=c.owner;
UPDATE reviews.audit SET data=jsonb_set(data,'{Actor}','"anonymous"')
 WHERE audit_action IN ('reviews.created.v1','reviews.replied.v1','reviews.republished.v1','reviews.withdraw.v1',
 'reviews.delete.v1','reviews.draft_saved.v1','reviews.voted.v1') AND audit_actor<>'anonymous';

-- Old closures may already be complete at the host. Their private drafts
-- and already-withdrawn discussions no longer contribute to public activity.
DELETE FROM reviews.drafts d USING reviews.closed_owners c WHERE d.owner=c.owner;
DELETE FROM reviews.entries e USING reviews.entries w,reviews.closed_owners c
 WHERE w.owner=c.owner AND w.state='withdrawn' AND (e.id=w.id OR (w.id=w.root_id AND e.root_id=w.id));
UPDATE reviews.identities i SET data=jsonb_set(i.data,'{Signed}','false') FROM reviews.closed_owners c
 WHERE i.subject=c.owner AND (i.data->>'Signed')::boolean;
UPDATE reviews.entries e SET data=jsonb_set(e.data,'{input,signed}','false') #- '{input,disclosures}'
 FROM reviews.closed_owners c WHERE e.owner=c.owner AND ((e.data#>>'{input,signed}')::boolean OR e.data#>'{input,disclosures}' IS NOT NULL);

-- Repair retained moderator decisions made before deletion cleared the withdrawal deadline.
UPDATE reviews.entries SET expires_at=NULL, data=jsonb_set(data,'{expires_at}','null')
 WHERE state IN ('deleted','hidden') AND expires_at IS NOT NULL;

CREATE INDEX IF NOT EXISTS reviews_published_teacher_replies ON reviews.entries(root_id,id)
 WHERE state='published' AND COALESCE((data->>'teacher_reply')::boolean,false);
CREATE INDEX IF NOT EXISTS reviews_activity_roots ON reviews.entries(kind,(data->>'activity_target_id'),id)
 WHERE state='published';

-- Occurrence instants and review visibility are shared by live writes and
-- calendar rebuilds. The caller checks or joins the discussion's root.
CREATE OR REPLACE FUNCTION reviews.activity_facts(root_filter text, as_of timestamptz)
RETURNS TABLE(root_id text, occurred_at timestamptz, reviews integer, interactions integer)
LANGUAGE sql STABLE AS $$
 SELECT e.root_id,e.first_published_at,CASE WHEN e.kind='course' THEN 1 ELSE 0 END,CASE WHEN e.kind='reply' THEN 1 ELSE 0 END
 FROM reviews.entries e
 WHERE (root_filter IS NULL OR e.root_id=root_filter)
   AND e.state='published' AND (e.expires_at IS NULL OR e.expires_at>as_of)
   AND e.first_published_at IS NOT NULL AND ((e.id=e.root_id AND e.kind='course') OR e.kind='reply')
 UNION ALL
 SELECT e.root_id,v.created_at,0,1
 FROM reviews.votes v JOIN reviews.entries e ON e.id=v.id
 WHERE (root_filter IS NULL OR e.root_id=root_filter)
   AND e.state='published' AND (e.expires_at IS NULL OR e.expires_at>as_of)
$$;

ALTER TABLE reviews.activity_retention ADD COLUMN IF NOT EXISTS time_zone text NOT NULL DEFAULT 'UTC'
 CHECK (time_zone IN ('UTC','Asia/Shanghai'));

-- Keep an old process or an early rollback restart from mixing calendars.
CREATE OR REPLACE FUNCTION reviews.guard_activity_calendar()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$
DECLARE expected_type text;
BEGIN
 SELECT CASE time_zone WHEN 'UTC' THEN 'discovery.activity.v1' ELSE 'discovery.activity.v2' END
 INTO STRICT expected_type FROM reviews.activity_retention WHERE singleton;
 IF NEW.data->>'type'<>expected_type THEN
  RAISE EXCEPTION 'reviews activity event calendar does not match storage';
 END IF;
 RETURN NULL;
END
$$;
REVOKE ALL ON FUNCTION reviews.guard_activity_calendar() FROM PUBLIC;
CREATE OR REPLACE TRIGGER guard_activity_calendar AFTER INSERT ON reviews.outbox FOR EACH ROW
WHEN ((NEW.data->>'type') IN ('discovery.activity.v1','discovery.activity.v2'))
EXECUTE FUNCTION reviews.guard_activity_calendar();

CREATE OR REPLACE FUNCTION reviews.set_activity_timezone(target_zone text)
RETURNS void LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE
 migration_time timestamptz := transaction_timestamp();
 event_type text;
BEGIN
 IF target_zone IS NULL OR target_zone NOT IN ('UTC','Asia/Shanghai') THEN
  RAISE EXCEPTION 'unsupported reviews activity calendar';
 END IF;
 PERFORM pg_advisory_xact_lock(hashtextextended('reviews:activity-calendar',0));
 IF EXISTS (SELECT 1 FROM reviews.activity_retention WHERE singleton AND time_zone=target_zone) THEN
  RETURN;
 END IF;

 -- Run while the application and relay are stopped. Concurrent writers cause
 -- the migration to fail rather than waiting while holding other table locks.
 LOCK TABLE reviews.entries,reviews.votes,reviews.activity_contributions,reviews.activity_buckets,
   reviews.activity_history,reviews.activity_retention,reviews.outbox IN SHARE ROW EXCLUSIVE MODE NOWAIT;
 event_type := CASE target_zone WHEN 'UTC' THEN 'discovery.activity.v1' ELSE 'discovery.activity.v2' END;
 UPDATE reviews.activity_retention SET time_zone=target_zone,revision=revision+1 WHERE singleton;

 DELETE FROM reviews.activity_contributions;
 WITH active_roots AS (
  SELECT e.id,e.school_id,e.kind,e.data FROM reviews.entries e
  WHERE e.id=e.root_id AND e.state='published' AND (e.expires_at IS NULL OR e.expires_at>migration_time)
 ), targets AS (
  SELECT r.id AS root_id,r.school_id,CASE r.kind WHEN 'course' THEN 'course' ELSE 'teacher' END AS kind,
         r.data->>'activity_target_id' AS target_id
  FROM active_roots r WHERE r.kind IN ('course','mentor')
  UNION
  SELECT r.id,r.school_id,'teacher',t.id
  FROM active_roots r
  CROSS JOIN LATERAL jsonb_array_elements_text(
   CASE WHEN jsonb_typeof(r.data->'teacher_ids')='array' THEN r.data->'teacher_ids' ELSE '[]'::jsonb END
  ) AS t(id)
  WHERE r.kind='course'
 ), root_counts AS (
  SELECT f.root_id,(f.occurred_at AT TIME ZONE target_zone)::date AS day,
         sum(f.reviews) AS reviews,sum(f.interactions) AS interactions
  FROM reviews.activity_facts(NULL,migration_time) f
  GROUP BY f.root_id,day
 )
 INSERT INTO reviews.activity_contributions(root_id,id,kind,target_id,school_id,day,reviews,interactions)
 SELECT t.root_id,t.kind||'/'||translate(encode(convert_to(t.target_id,'UTF8'),'base64'),'+/='||chr(10),'-_')
            ||'/'||to_char(c.day,'YYYY-MM-DD'),
        t.kind,t.target_id,t.school_id,c.day,c.reviews,c.interactions
 FROM targets t JOIN root_counts c USING(root_id)
 WHERE octet_length(t.target_id) BETWEEN 1 AND 200;

 -- Complete snapshots supersede previously queued snapshots under the other
 -- calendar. Keep the outbox rows and leave unrelated content events pending.
 UPDATE reviews.outbox SET sent_at=migration_time
 WHERE sent_at IS NULL AND data->>'type' IN ('discovery.activity.v1','discovery.activity.v2')
   AND data->>'type'<>event_type;

 WITH totals AS (
  SELECT id,kind,target_id,school_id,day,sum(reviews)::bigint AS reviews,sum(interactions)::bigint AS interactions
  FROM reviews.activity_contributions GROUP BY id,kind,target_id,school_id,day
 ), next AS (
  SELECT coalesce(t.id,b.id) AS id,coalesce(t.kind,b.kind) AS kind,coalesce(t.target_id,b.target_id) AS target_id,
         coalesce(t.school_id,b.school_id) AS school_id,coalesce(t.day,b.day) AS day,
         coalesce(t.reviews,0) AS reviews,coalesce(t.interactions,0) AS interactions,
         coalesce(b.revision,0)+1 AS revision,t.id IS NOT NULL AS visible
  FROM totals t FULL JOIN reviews.activity_buckets b USING(id)
 ), updated AS (
  INSERT INTO reviews.activity_buckets(id,kind,target_id,school_id,day,first_count,second_count,revision,visible,data)
  SELECT id,kind,target_id,school_id,day,reviews,interactions,revision,visible,
         jsonb_build_object('source','reviews','id',id,'day',to_char(day,'YYYY-MM-DD'),
          'target',jsonb_build_object('source','catalog','kind',kind,'id',target_id),
          'metrics',jsonb_build_object('reviews',reviews,'interactions',interactions),
          'revision',revision,'visible',visible)
  FROM next
  ON CONFLICT(id) DO UPDATE SET first_count=excluded.first_count,second_count=excluded.second_count,
    revision=excluded.revision,visible=excluded.visible,data=excluded.data,school_id=excluded.school_id
  RETURNING id,revision,day,data
 ), history AS (
  INSERT INTO reviews.activity_history(id,revision,day,data)
  SELECT id,revision,day,data FROM updated RETURNING id,revision,data
 ), events AS (
  SELECT 'activity:'||encode(sha256(convert_to(id,'UTF8')),'hex')||':'||revision AS event_id,id,data
  FROM history
 )
 INSERT INTO reviews.outbox(id,data)
 SELECT event_id,jsonb_build_object('specversion','1.0','id',event_id,'source','reviews','type',event_type,
    'subject',id,'time',migration_time,'data',data)
 FROM events;

 IF EXISTS (
  SELECT 1 FROM reviews.activity_buckets
  WHERE first_count NOT BETWEEN 0 AND 1000000000 OR second_count NOT BETWEEN 0 AND 1000000000
 ) THEN
  RAISE EXCEPTION 'reviews activity count exceeds snapshot contract';
 END IF;

 PERFORM pg_notify('reviews_outbox','');
END
$$;
REVOKE ALL ON FUNCTION reviews.set_activity_timezone(text) FROM PUBLIC;

SELECT reviews.set_activity_timezone('Asia/Shanghai');
