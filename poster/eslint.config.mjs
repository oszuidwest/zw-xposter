import js from '@eslint/js';

// ESLint's defaults already cover node_modules, .mjs files and ES modules.
export default [
  js.configs.recommended,
  {
    languageOptions: {
      globals: {
        AbortController: 'readonly',
        AbortSignal: 'readonly',
        Buffer: 'readonly',
        console: 'readonly',
        document: 'readonly',
        fetch: 'readonly',
        process: 'readonly',
        URL: 'readonly',
      },
    },
  },
];
