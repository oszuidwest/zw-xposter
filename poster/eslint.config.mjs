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
        Blob: 'readonly',
        console: 'readonly',
        fetch: 'readonly',
        FormData: 'readonly',
        Response: 'readonly',
        process: 'readonly',
        URL: 'readonly',
        URLSearchParams: 'readonly',
      },
    },
  },
];
