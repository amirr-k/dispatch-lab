drop index if exists simulations_owner_idx;
drop index if exists simulations_expiry_idx;

alter table simulations drop column if exists expires_at;
alter table simulations drop column if exists guest_token;

drop index if exists guest_sessions_expires_idx;
drop table if exists guest_sessions;
