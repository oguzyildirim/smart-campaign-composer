-- Users, channel consent, and the behavioural events that segment rules
-- filter on. Every recipient is fake: the CHECK constraints below reject
-- any email outside the reserved example.com domain and any phone number
-- outside the documented fake range (+999 is an unassigned ITU-T country
-- code), so a real address cannot be stored even by mistake.

-- city_key folds a city name for case-insensitive matching. lower() alone
-- is collation-dependent for the Turkish dotted/dotless I, so Turkish
-- letters are mapped to ASCII first: 'İstanbul', 'ISTANBUL', and
-- 'istanbul' all yield 'istanbul'. Segment queries apply it to both the
-- column and the user-supplied values (passed as parameters).
CREATE FUNCTION city_key(name text) RETURNS text
    LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
    RETURN lower(translate(btrim(name), 'İIıŞşĞğÜüÖöÇçÂâÎîÛû', 'iiissgguuooccaaiiuu'));

CREATE TABLE users (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    first_name text        NOT NULL CHECK (char_length(first_name) BETWEEN 1 AND 100),
    last_name  text        NOT NULL CHECK (char_length(last_name) BETWEEN 1 AND 100),
    email      text        NOT NULL UNIQUE CHECK (email ~ '^[a-z0-9._+-]+@example\.com$'),
    phone      text        NOT NULL UNIQUE CHECK (phone ~ '^\+9995[0-9]{9}$'),
    city       text        NOT NULL CHECK (char_length(city) BETWEEN 1 AND 64),
    locale     text        NOT NULL CHECK (locale IN ('tr', 'en')),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX users_city_key_idx ON users (city_key(city));

-- One row per (user, channel). A missing row means "never asked" and is
-- treated exactly like granted = false: no consent, no message.
CREATE TABLE channel_consents (
    user_id    bigint      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    channel    text        NOT NULL CHECK (channel IN ('sms', 'email')),
    granted    boolean     NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, channel)
);

CREATE TABLE cart_events (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id     bigint      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    product_sku text        NOT NULL CHECK (char_length(product_sku) BETWEEN 1 AND 64),
    added_at    timestamptz NOT NULL
);

-- Segment predicates are per-user EXISTS lookups bounded by time.
CREATE INDEX cart_events_user_added_idx ON cart_events (user_id, added_at);

CREATE TABLE orders (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id      bigint        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    total_amount numeric(12, 2) NOT NULL CHECK (total_amount >= 0),
    created_at   timestamptz   NOT NULL
);

CREATE INDEX orders_user_created_idx ON orders (user_id, created_at);
