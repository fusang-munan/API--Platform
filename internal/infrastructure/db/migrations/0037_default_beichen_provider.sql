-- Keep URL detection and name allocation consistent with concurrent provider writes.
LOCK TABLE providers IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
  provider_name citext := '北辰';
  name_suffix integer := 2;
BEGIN
  IF EXISTS (
    SELECT 1 FROM providers
    WHERE regexp_replace(
      btrim(base_url, E' \t\n\r'),
      '^https://api[.]beichenapi[.]com(?=/|$)',
      'https://api.beichenapi.com', 'i'
    ) IN ('https://api.beichenapi.com', 'https://api.beichenapi.com/',
          'https://api.beichenapi.com/v1', 'https://api.beichenapi.com/v1/')
  ) THEN
    RETURN;
  END IF;

  WHILE EXISTS (SELECT 1 FROM providers WHERE name = provider_name) LOOP
    provider_name := '北辰 (' || name_suffix || ')';
    name_suffix := name_suffix + 1;
  END LOOP;

  -- Migration 0008 already moved secrets out of providers. Leave credentials empty.
  INSERT INTO providers (id, name, base_url, status, created_at, updated_at)
  VALUES ('01a1ee54-fd9a-7028-9fb2-24d9989952a9', provider_name,
    'https://api.beichenapi.com', 'active', now(), now());
END $$;
