import * as globals from 'globals'
import js from '@eslint/js'
import ts from 'typescript-eslint'
import vue from 'eslint-plugin-vue'
import vueParser from 'vue-eslint-parser'
import { defineConfig } from 'eslint/config'

export default defineConfig([
  // Base configs (replace "extends")
  js.configs.recommended,
  ...ts.configs.recommended,

  // JS/TS files
  {
    files: ['**/*.{ts,tsx,js,jsx}'],
    languageOptions: {
      parser: ts.parser,
      parserOptions: {
        ecmaVersion: 2022,
        sourceType: 'module',
      },
      globals: { ...globals.browser, ...globals.es2022 },
    },
    rules: {
      '@typescript-eslint/no-unused-vars': [
        'error',
        { argsIgnorePattern: '^_', varsIgnorePattern: '^_', caughtErrorsIgnorePattern: '^_' },
      ],
      '@typescript-eslint/no-explicit-any': 'warn',
      '@typescript-eslint/consistent-type-imports': ['error', { prefer: 'type-imports' }],
      'prefer-const': 'error',
      'no-var': 'error',
    },
  },

  // Vue config (replace extends)
  ...vue.configs['flat/recommended'],

  {
    files: ['**/*.vue'],
    languageOptions: {
      parser: vueParser,
      parserOptions: {
        parser: ts.parser,
        extraFileExtensions: ['.vue'],
        ecmaVersion: 2022,
        sourceType: 'module',
      },
    },
    rules: {
      '@typescript-eslint/no-unused-vars': 'off',
      'vue/no-unused-vars': 'error',
      'vue/multi-word-component-names': 'off',
      'vue/html-self-closing': [
        'error',
        { html: { void: 'always', normal: 'never', component: 'always' } },
      ],
      'vue/component-name-in-template-casing': ['error', 'PascalCase'],
      'vue/no-v-html': 'off',
      'vue/require-default-prop': 'off',
      'vue/define-macros-order': ['error', { order: ['defineProps', 'defineEmits'] }],
    },
  },

  { ignores: ['dist/**', 'node_modules/**', '*.d.ts'] },
])
