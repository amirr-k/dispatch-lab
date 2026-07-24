-- Anonymous visitor sessions. A token is the only identity the public demo
-- has: it scopes what a visitor can see and bounds what they can create.
create table if not exists guest_sessions (
    token        text primary key,
    created_at   timestamptz not null default now(),
    last_seen_at timestamptz not null default now(),
    expires_at   timestamptz not null
);

create index if not exists guest_sessions_expires_idx
    on guest_sessions (expires_at);

-- Runs belong to the session that created them. On delete set null rather
-- than cascade: a showcase run outlives the session that produced it, which
-- is the whole point of a stable replay URL.
alter table simulations
    add column if not exists guest_token text references guest_sessions (token) on delete set null;

-- When an anonymous run becomes eligible for pruning. Null for showcase runs,
-- which are never pruned.
alter table simulations
    add column if not exists expires_at timestamptz;

create index if not exists simulations_expiry_idx
    on simulations (showcase, expires_at);

create index if not exists simulations_owner_idx
    on simulations (guest_token);
