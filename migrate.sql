-- migrate.sql
CREATE TABLE IF NOT EXISTS voters (
  id SERIAL PRIMARY KEY,
  code TEXT UNIQUE NOT NULL,
  name TEXT NOT NULL,
  used BOOLEAN NOT NULL DEFAULT FALSE,
  used_at TIMESTAMPTZ,
  vote_choice TEXT
);

-- example seed
INSERT INTO voters (code, name) VALUES
('Ht67h', 'Titus Prasetyo') ON CONFLICT DO NOTHING,
('Ab12X', 'Budi Santoso') ON CONFLICT DO NOTHING,
('Z9yQ1', 'Siti Nurhayati') ON CONFLICT DO NOTHING;
