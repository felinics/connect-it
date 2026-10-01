create table if not exists connector_settings (
  connector_type text primary key,
  enabled boolean not null default true,
  updated_at timestamptz not null
);
