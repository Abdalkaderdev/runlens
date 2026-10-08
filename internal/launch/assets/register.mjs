import { register, createRequire } from 'node:module';

const tool = /[\/](npm|npx|pnpm|pnpx|yarn|corepack)(-cli)?(\.c?js)?$|[\/]node_modules[\/](npm|pnpm|yarn|corepack)[\/]/;

if (!tool.test(process.argv[1] ?? '')) {
  register('@opentelemetry/instrumentation/hook.mjs', import.meta.url);
  createRequire(import.meta.url)('@opentelemetry/auto-instrumentations-node/register');
}
