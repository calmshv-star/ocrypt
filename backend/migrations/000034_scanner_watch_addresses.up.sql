CREATE OR REPLACE FUNCTION scanner_active_watch_addresses(
  requested_chain text,
  observed_at timestamptz
) RETURNS TABLE(address text)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,public
AS $$
  SELECT DISTINCT CASE
    WHEN requested_chain LIKE 'eip155:%' THEN lower(r.receiving_address)
    ELSE r.receiving_address
  END AS address
  FROM public.payment_routes r
  WHERE r.provider='on_chain'
    AND r.chain_id=requested_chain
    AND r.receiving_address IS NOT NULL
    AND r.status IN ('active','expired')
    AND r.starts_at<=observed_at
    AND r.grace_ends_at>observed_at
  ORDER BY 1
$$;

REVOKE ALL ON FUNCTION scanner_active_watch_addresses(text,timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION scanner_active_watch_addresses(text,timestamptz) TO merchant_scanner_worker;
