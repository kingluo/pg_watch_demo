CREATE TABLE IF NOT EXISTS config (
	key text,
	value text,
	revision bigserial,
	tombstone boolean NOT NULL DEFAULT false,
	primary key(key, revision)
);

drop function if exists get1;
drop function if exists get;
drop function if exists set;
drop function if exists del;
drop function if exists get_all;
drop function if exists get_all_from_rev_with_stale;
drop trigger if exists notify_config_change on config;
drop function if exists notify_config_change;

CREATE FUNCTION get1(kk text)
RETURNS table(r bigint, k text, v text) AS $$
    SELECT revision, key, value
    FROM config
    where key = kk and tombstone = false
    ORDER BY key, revision desc
    limit 1
$$ LANGUAGE sql;

CREATE FUNCTION get(kk text, rev bigint default 0)
RETURNS table(r bigint, k text, v text) AS $$
BEGIN
	return query with c as (
		SELECT DISTINCT ON (key)
			   revision, key, value, tombstone
		FROM config
		where key = kk and (rev = 0 or revision = rev)
		ORDER BY key, revision DESC
	) select revision, key, value from c where tombstone = false;
END;
$$ LANGUAGE plpgsql;

CREATE FUNCTION set(k text, v text) RETURNS bigint AS $$
	insert into config(key, value) values(k, v) returning revision;
$$ LANGUAGE SQL;

CREATE FUNCTION del(k text) RETURNS bigint AS $$
	insert into config(key, tombstone) values(k, true) returning revision;
$$ LANGUAGE SQL;

CREATE FUNCTION get_all(prefix text)
RETURNS table(r bigint, k text, v text) AS $$
declare
	v_prefix text = prefix || '%';
BEGIN
	return query with c as (
		SELECT DISTINCT ON (key)
			   revision, key, value, tombstone
		FROM config
		where key like v_prefix
		ORDER BY key, revision DESC
	) select revision, key, value from c where tombstone = false;
END;
$$ LANGUAGE plpgsql;

CREATE FUNCTION get_all_from_rev_with_stale(prefix text, rev bigint)
RETURNS table(r bigint, k text, v text, tomb boolean) AS $$
declare
	v_prefix text = prefix || '%';
BEGIN
	return query with c as (
		SELECT DISTINCT ON (key)
			   revision, key, value, tombstone
		FROM config
		where key like v_prefix and revision > rev
		ORDER BY key, revision DESC
	) select revision, key, value, tombstone from c;
END;
$$ LANGUAGE plpgsql;

CREATE FUNCTION notify_config_change() RETURNS TRIGGER AS $$
DECLARE
	data json;
	channel text;
    is_channel_exist boolean;
BEGIN
	IF (TG_OP = 'INSERT') THEN
		data = row_to_json(NEW);
		channel = (select SUBSTRING(NEW.key, '/(.*)/'));
        --perform 1 from pg_stat_activity where wait_event = 'ClientRead' and query ~* ('LISTEN "' || channel || '"') limit 1;
        is_channel_exist = not pg_try_advisory_lock(9080);
        if is_channel_exist then
            PERFORM pg_notify(channel, data::text);
        else
            perform pg_advisory_unlock(9080);
        end if;
	END IF;
	RETURN NULL; -- result is ignored since this is an AFTER trigger
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER notify_config_change
AFTER INSERT ON config
	FOR EACH ROW EXECUTE FUNCTION notify_config_change();
