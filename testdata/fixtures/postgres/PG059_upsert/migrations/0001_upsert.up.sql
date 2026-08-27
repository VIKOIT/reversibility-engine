INSERT INTO app_settings (key, value) VALUES ('feature.checkout_v2', 'on') ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value;
