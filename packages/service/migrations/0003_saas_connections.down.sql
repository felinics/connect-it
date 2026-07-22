alter table oauth_authorizations drop column redirect_url;
update connections set alias = id::text where alias is null;
alter table connections alter column alias set not null;
alter table connections add constraint connections_alias_key unique (alias);
