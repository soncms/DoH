/* eslint-env node */
module.exports = {
  root: true,
  env: { browser: true, node: true, es2022: true },
  extends: ["eslint:recommended", "plugin:vue/vue3-essential"],
  plugins: ["vue"],
  parserOptions: { ecmaVersion: "latest", sourceType: "module" },
  rules: {
    "vue/multi-word-component-names": "off",
    "vue/require-default-prop": "off",
  },
};
