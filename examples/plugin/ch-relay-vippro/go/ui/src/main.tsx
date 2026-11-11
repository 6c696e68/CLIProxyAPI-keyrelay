import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { App } from './App';
import { bootHost } from './host';
import './app.css';

// Before the first paint: follow the panel theme and reserve the panel header band.
bootHost((value) => {
  document.documentElement.dataset.theme = value;
});

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>
);
