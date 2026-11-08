# Translations

Sapu explains every item it finds in plain language: what it is, whether it is
safe to remove, and what happens next. Those explanations come in seven
languages.

| Code | Language              |
|------|-----------------------|
| `en` | English (the source)  |
| `zh` | Chinese (Simplified)  |
| `ja` | Japanese              |
| `id` | Indonesian            |
| `es` | Spanish               |
| `pt` | Portuguese (Brazil)   |
| `de` | German                |

## How the language is picked

Sapu reads three environment variables and uses the first one that is set:

1. `SAPU_LANG`
2. `LC_ALL`
3. `LANG`

Only the first two letters count, in lower case. `id_ID.UTF-8` becomes `id`,
`ja_JP.UTF-8` becomes `ja`. The values `C`, `POSIX` and `C.*` are ignored. If
nothing is set, or the language has no translation for a text, Sapu shows the
English text. (The code is `langFrom` in `internal/env/env.go`.)

Try it:

```
SAPU_LANG=id sapu clean --plan plan.json
```

Because only two letters are read, `zh_TW` also gets Simplified Chinese and
`pt_PT` gets Brazilian Portuguese.

## What is translated

Only the texts that describe an item:

- `explain` of every cleanup rule in `rules/cleaners/*.yml`
- `explain` of every build-artifact type in `rules/purge/types.yml`
- `reason` of every protected folder in `rules/protect/core.yml`

These are what you read in a plan, in `--json` output and in the "skipped"
list, so they are what a person needs to decide.

## What is still English

All screen text of the program: headings, prompts, confirmations, error and
status messages, `--help`, and the documentation. Only the item texts above
change with the language.

Some words stay in English on purpose, in every language:

- app and product names (Chrome, Xcode, CapCut, Homebrew)
- file and folder names (`workspaceStorage`)
- commands, written between backticks in the English text (`brew cleanup`,
  `go clean -modcache`)
- terms people see in their own tools (Cache, build, commit, hook)
- the issue references at the end of some protect reasons, such as
  `(V2EX 1163304)`

A translation keeps the same meaning as the English: what it is, whether it is
safe, what happens next. It must not sound safer or less safe than the English.

## How to add a language

1. Pick the two-letter code, for example `fr`.
2. In each of the three files, add a `fr:` line next to every `en:` line, with
   the same indentation. Keep the other fields as they are.

   ```yaml
   explain:
     en: "npm's download cache. Safe to remove; ..."
     fr: "Cache de téléchargement de npm. ..."
   ```
3. Add the code to `translatedLangs` in `internal/rules/i18n_test.go`.
4. Run `make check`. The test `TestTextsAreTranslated` tells you which text is
   missing, empty, equal to the English, or has lost a command from between
   backticks.
5. Add the language to the table above.

The test requires that every text has all languages, so a language is added for
all texts at once, never for some of them.

## Review by native speakers

The translations were written to be plain and friendly, but they have not been
checked by native speakers yet. Please have someone review them, most of all:

- **Indonesian (`id`)**: should sound like everyday speech, not stiff. Uses
  "kamu" where a pronoun is needed.
- **Chinese (`zh`)**: Simplified, as used on the mainland. Uses "你".
- **Japanese (`ja`)**: polite form (です/ます).

Spanish is neutral (uses "tú"), Portuguese is Brazilian ("você"), and German
avoids "Sie" by using impersonal wording, as macOS does.

macOS names (Notes, Messages, Voice Memos, Keychain and so on) use Apple's
wording where it was known, and English where it was not. Reviewers can check
those too.
