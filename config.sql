CREATE TABLE IF NOT EXISTS config (
	key text,
	value text,
	revision bigserial,
	tombstone boolean NOT NULL DEFAULT false,
	create_time bigint NOT NULL DEFAULT (EXTRACT(EPOCH FROM now()) * 1000)::bigint,
	primary key(key, revision)
);

CREATE INDEX IF NOT EXISTS idx_config_create_time on config (create_time);

drop function if exists get;
drop function if exists set;
drop function if exists del;
drop function if exists get_all;
drop function if exists get_all_from_rev_with_stale;
drop trigger if exists notify_config_change on config;
drop function if exists notify_config_change;

CREATE FUNCTION get(kk text, rev bigint default 0)
RETURNS table(r bigint, k text, v text, c bigint) AS $$
BEGIN
	return query with c as (
		SELECT DISTINCT ON (key)
			   revision, key, value, create_time, tombstone
		FROM config
		where key = kk and (rev = 0 or revision = rev)
		ORDER BY key, revision DESC
	) select revision, key, value, create_time from c where tombstone = false;
END;
$$ LANGUAGE plpgsql;

CREATE FUNCTION set(k text, v text) RETURNS bigint AS $$
	insert into config(key, value) values(k, v) returning revision;
$$ LANGUAGE SQL;

CREATE FUNCTION del(k text) RETURNS bigint AS $$
	insert into config(key, tombstone) values(k, true) returning revision;
$$ LANGUAGE SQL;

CREATE FUNCTION get_all(prefix text)
RETURNS table(r bigint, k text, v text, c bigint) AS $$
declare
	v_prefix text = prefix || '%';
BEGIN
	return query with c as (
		SELECT DISTINCT ON (key)
			   revision, key, value, create_time, tombstone
		FROM config
		where key like v_prefix
		ORDER BY key, revision DESC
	) select revision, key, value, create_time from c where tombstone = false;
END;
$$ LANGUAGE plpgsql;

CREATE FUNCTION get_all_from_rev_with_stale(prefix text, rev bigint)
RETURNS table(r bigint, k text, v text, c bigint, tomb boolean) AS $$
declare
	v_prefix text = prefix || '%';
BEGIN
	return query with c as (
		SELECT DISTINCT ON (key)
			   revision, key, value, create_time, tombstone
		FROM config
		where key like v_prefix and revision > rev
		ORDER BY key, revision DESC
	) select revision, key, value, create_time, tombstone from c;
END;
$$ LANGUAGE plpgsql;

CREATE FUNCTION notify_config_change() RETURNS TRIGGER AS $$
DECLARE
	data json;
	channel text;
BEGIN
	IF (TG_OP = 'INSERT') THEN
		data = row_to_json(NEW);
		channel = (select SUBSTRING(NEW.key, '/(.*)/'));
		PERFORM pg_notify(channel, data::text);
	END IF;
	RETURN NULL; -- result is ignored since this is an AFTER trigger
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER notify_config_change
AFTER INSERT ON config
	FOR EACH ROW EXECUTE FUNCTION notify_config_change();
