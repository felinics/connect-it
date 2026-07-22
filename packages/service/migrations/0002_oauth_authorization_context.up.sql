alter table oauth_authorizations
  add column auth_method text not null default '',
  add column alias text not null default '',
  add column secret_key_version integer not null default 0;
