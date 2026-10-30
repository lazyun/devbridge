import { strict as assert } from "node:assert";
import test from "node:test";
import { dictionaries, languages, resolveLang, translate } from "../src/i18n.js";

test("en and zh-CN dictionaries expose the same keys", () => {
  const en = Object.keys(dictionaries.en).sort();
  const zh = Object.keys(dictionaries["zh-CN"]).sort();
  assert.deepEqual(zh, en);
});

test("every key resolves in both languages with non-empty text", () => {
  for (const key of Object.keys(dictionaries.en)) {
    for (const lang of ["en", "zh-CN"]) {
      const text = translate(lang, key);
      assert.ok(text && text !== key, `${lang}/${key} unresolved`);
    }
  }
});

test("system preference follows the platform locale", () => {
  assert.equal(resolveLang("system", "zh-CN"), "zh-CN");
  assert.equal(resolveLang("system", "zh_TW"), "zh-CN");
  assert.equal(resolveLang("system", "en-US"), "en");
  assert.equal(resolveLang("system", "fr"), "en");
  assert.equal(resolveLang("system", ""), "en");
});

test("explicit preference overrides the system locale", () => {
  for (const language of languages) {
    assert.equal(resolveLang(language, "zh-CN"), language === "system" ? "zh-CN" : language);
  }
});

test("interpolation replaces named placeholders", () => {
  assert.equal(
    translate("zh-CN", "app.subtitle", { version: "9.9.9" }),
    "本地操作面板 · Desktop 9.9.9",
  );
});
