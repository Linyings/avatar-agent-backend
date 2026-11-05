# Vendored Chinese Humanizer

Source: https://github.com/holygeek00/humanizer-zh-cn

Installed and copied on 2026-10-24 using the Codex skill installer. Upstream metadata name: `humanizer-zh`; version: `2.9.1-zh.2`. Local installation alias: `humanizer-zh-cn`.

`SKILL.md` and `LICENSE` are unmodified upstream files. SKILL.md SHA-256: `494848f7a211af06f7b59061011478fd3c49cb5fe40ceda94a47c7bacf8481f4`.

The backend embeds the skill into general-mode manuscript writing only. Selected style templates remain authoritative for voice, with factual grounding, JSON schema, original-clip markers and local repair boundaries taking priority. This is a single-request embedded edit, not an additional model call. Chapter splitting and picture matching do not receive this skill.

Updating the vendored skill requires reviewing the prompt constraints and rerunning the general Humanizer tests. The effective prompt fingerprint includes the skill and selected template to prevent replaying older drafts under new writing rules.
