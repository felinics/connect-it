-- SaaS 授权模型：连接 ID 即句柄。
-- alias 降级为可选展示标签（不唯一）；授权行记录调用方的回跳地址。
alter table connections drop constraint connections_alias_key;
alter table connections alter column alias drop not null;
alter table oauth_authorizations add column redirect_url text not null default '';
