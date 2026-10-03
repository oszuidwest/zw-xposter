const results = await Promise.allSettled([
  'http://127.0.0.1:8080/health',
  'http://127.0.0.1:8081/ready',
].map(async (url) => {
  const response = await fetch(url, { signal: AbortSignal.timeout(4000) });
  await response.body?.cancel();
  return response.ok;
}));
process.exit(results.every((result) => result.status === 'fulfilled' && result.value) ? 0 : 1);
