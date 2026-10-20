import { mergeConfig } from 'vite';
import base from './vite.config';

export default mergeConfig(base, {
  server: {
    host: '127.0.0.1',
    port: 5173,
    strictPort: true,
    proxy: Object.fromEntries(
      ['/v8', '/v0', '/v1', '/client', '/key-dashboard'].map((path) => [
        path,
        { target: 'http://127.0.0.1:8319', ws: true },
      ])
    ),
  },
});
