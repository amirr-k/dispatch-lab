-- Simulation metadata. A run is retained permanently only once it is marked
-- as a showcase; anonymous guest runs are expected to be pruned later.
create table if not exists simulations (
    id           text primary key,
    seed         bigint      not null,
    drivers      integer     not null,
    strategy     text        not null,
    created_at   timestamptz not null default now(),
    completed_at timestamptz,
    showcase     boolean     not null default false
);

create index if not exists simulations_showcase_idx
    on simulations (showcase, created_at desc);

-- The append-only event log. (simulation_id, sequence) is the primary key, so
-- a retried flush of an overlapping batch cannot duplicate a row. Payloads are
-- jsonb because the store never interprets them and a new event type must not
-- require a migration.
create table if not exists simulation_events (
    simulation_id text             not null references simulations (id) on delete cascade,
    sequence      integer          not null,
    virtual_time  double precision not null,
    type          text             not null,
    payload       jsonb            not null,
    trace_id      text,
    recorded_at   timestamptz      not null default now(),
    primary key (simulation_id, sequence)
);

-- Periodic full-state snapshots, so replay can start near a target sequence
-- instead of folding the log from zero.
create table if not exists simulation_snapshots (
    simulation_id text             not null references simulations (id) on delete cascade,
    sequence      integer          not null,
    virtual_time  double precision not null,
    payload       jsonb            not null,
    recorded_at   timestamptz      not null default now(),
    primary key (simulation_id, sequence)
);

-- Algorithm comparison results, stored whole so a published number can always
-- be traced back to the run that produced it.
create table if not exists comparisons (
    id         text primary key,
    seed       bigint      not null,
    drivers    integer     not null,
    result     jsonb       not null,
    created_at timestamptz not null default now()
);
