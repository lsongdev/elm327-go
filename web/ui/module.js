// elm327-go entry point (panel module contract, version 1). The host is a
// panel shell, or standalone.js on its own.
import { createMount } from './lib/mount.js';

const routes = [
  { path: '/', load: () => import('./pages/dashboard.js') },
  { path: '/sensors', load: () => import('./pages/sensors.js') },
  { path: '/diagnostics', load: () => import('./pages/diagnostics.js') },
  { path: '/vehicle', load: () => import('./pages/vehicle.js') },
  { path: '/console', load: () => import('./pages/console.js') },
];

// The API answers plain JSON bodies, not {"data": …}.
export const mount = createMount({ routes, envelope: false });
