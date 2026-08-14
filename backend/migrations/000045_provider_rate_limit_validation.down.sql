BEGIN;

CREATE OR REPLACE FUNCTION platform_exact_money_strings(value jsonb) RETURNS boolean
LANGUAGE plpgsql IMMUTABLE PARALLEL SAFE AS $$
DECLARE k text; v jsonb;
BEGIN
    IF jsonb_typeof(value) = 'object' THEN
        FOR k,v IN SELECT item.key,item.value FROM jsonb_each(value) AS item(key,value) LOOP
            IF k ~* '(amount|balance|minimum|maximum|threshold|dust|fee|limit)'
               AND jsonb_typeof(v) NOT IN ('string','null') THEN RETURN false; END IF;
            IF jsonb_typeof(v) = 'string' AND k ~* '(amount|balance|minimum|maximum|threshold|dust|fee|limit)'
               AND trim(both '"' from v::text) !~ '^(0|[1-9][0-9]{0,77})$' THEN RETURN false; END IF;
            IF NOT platform_exact_money_strings(v) THEN RETURN false; END IF;
        END LOOP;
    ELSIF jsonb_typeof(value) = 'array' THEN
        FOR v IN SELECT item.value FROM jsonb_array_elements(value) AS item(value) LOOP
            IF NOT platform_exact_money_strings(v) THEN RETURN false; END IF;
        END LOOP;
    END IF;
    RETURN true;
END $$;

COMMIT;
