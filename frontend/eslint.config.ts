import * as globals from 'globals'
import * as pluginJs from '@eslint/js'
import tseslint from 'typescript-eslint'
import * as pluginVue from 'eslint-plugin-vue'

export default tseslint.config(
  // Base JS rules
  pluginJs.configs.recommended,

  // TypeScript rules
  ...tseslint.configs.recommended,

  // Vue 3 rules
  ...pluginVue.configs['flat/recommended'],

  {
    languageOptions: {
      globals: { ...globals.browser, ...globals.es2022 },
      parserOptions: {
        parser: tseslint.parser,
        extraFileExtensions: ['.vue'],
        sourceType: 'module',
      },
    },

    rules: {
      // TypeScript
      '@typescript-eslint/no-unused-vars': [
        'error',
        { argsIgnorePattern: '^_', varsIgnorePattern: '^_', caughtErrorsIgnorePattern: '^_' },
      ],
      '@typescript-eslint/no-explicit-any': 'warn',
      '@typescript-eslint/consistent-type-imports': ['error', { prefer: 'type-imports' }],

      // Vue
      'vue/multi-word-component-names': 'off',
      'vue/no-unused-vars': 'error',
      'vue/html-self-closing': ['error', { html: { void: 'always', normal: 'never', component: 'always' } }],
      'vue/component-name-in-template-casing': ['error', 'PascalCase'],
      'vue/no-v-html': 'off', // we use v-html for log highlighting
      'vue/require-default-prop': 'off',
      'vue/define-macros-order': ['error', { order: ['defineProps', 'defineEmits'] }],

      // General
      'no-console': ['warn', { allow: ['warn', 'error'] }],
      'prefer-const': 'error',
      'no-var': 'error',
    },
  },

  {
    files: ['*.config.ts', '*.config.js', 'eslint.config.js'],
    rules: {
      '@typescript-eslint/no-require-imports': 'off',
    },
  },

  {
    ignores: ['dist/**', 'node_modules/**', '*.d.ts'],
  },
)