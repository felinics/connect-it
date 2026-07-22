alter table oauth_authorizations
  drop column auth_method,
  drop column alias,
  drop column secret_key_version;
