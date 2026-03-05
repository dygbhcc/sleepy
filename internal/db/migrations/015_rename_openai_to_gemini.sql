ALTER TABLE worker_settings RENAME COLUMN openai_api_key TO gemini_api_key;
ALTER TABLE worker_settings RENAME COLUMN openai_base_url TO gemini_base_url;
ALTER TABLE worker_settings RENAME COLUMN openai_model TO gemini_model;
