import * as globals from 'globals'
import { defineConfig } from 'eslint/config'
import tsParser from '@typescript-eslint/parser'

export default defineConfig([
  // Base for JS/TS files
  {
    files: ['**/*.{ts,tsx,js,jsx}'],
    extends: ['eslint:recommended', 'plugin:@typescript-eslint/recommended'],
    languageOptions: {
      parser: tsParser,
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
      'no-console': ['warn', { allow: ['warn', 'error'] }],
      'prefer-const': 'error',
      'no-var': 'error',
    },
  },

  // Vue files
  {
    files: ['**/*.vue'],
    languageOptions: {
      parser: 'vue-eslint-parser',
      parserOptions: {
        parser: tsParser,
        extraFileExtensions: ['.vue'],
        ecmaVersion: 2022,
        sourceType: 'module',
      },
    },
    extends: ['plugin:vue/flat/recommended'],
    rules: {
      // **Disable TS unused-vars completely for Vue**
      '@typescript-eslint/no-unused-vars': 'off',
      // Use Vue-aware rule
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
