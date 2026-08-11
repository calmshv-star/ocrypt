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

CREATE OR REPLACE FUNCTION platform_payload_has_no_secrets(value jsonb) RETURNS boolean
LANGUAGE plpgsql IMMUTABLE PARALLEL SAFE AS $$
DECLARE k text; v jsonb; s text;
BEGIN
    IF jsonb_typeof(value) = 'object' THEN
        FOR k,v IN SELECT item.key,item.value FROM jsonb_each(value) AS item(key,value) LOOP
            IF k ~* '(^|_)(private_?key|mnemonic|seed|password|secret|api_?key|access_?token|credential|signing_?key)($|_)'
               AND k !~* '(_ref|_reference)$' THEN RETURN false; END IF;
            IF NOT platform_payload_has_no_secrets(v) THEN RETURN false; END IF;
        END LOOP;
    ELSIF jsonb_typeof(value) = 'array' THEN
        FOR v IN SELECT item.value FROM jsonb_array_elements(value) AS item(value) LOOP
            IF NOT platform_payload_has_no_secrets(v) THEN RETURN false; END IF;
        END LOOP;
    ELSIF jsonb_typeof(value) = 'string' THEN
        s := trim(both '"' from value::text);
        IF s ~* '(BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY|(^|[[:space:]])(xprv|[KL][1-9A-HJ-NP-Za-km-z]{50,})($|[[:space:]]))' THEN RETURN false; END IF;
    END IF;
    RETURN true;
END $$;

COMMIT;
