import js from '@eslint/js';

export default [
  js.configs.recommended,
  {
    languageOptions: {
      globals: {
        AbortController: 'readonly',
        AbortSignal: 'readonly',
        AsyncDisposableStack: 'readonly',
        Buffer: 'readonly',
        console: 'readonly',
        document: 'readonly',
        fetch: 'readonly',
        FormData: 'readonly',
        process: 'readonly',
        URL: 'readonly',
      },
    },
  },
];
