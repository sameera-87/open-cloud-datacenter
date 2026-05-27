import { useContext } from 'react';
import { ApiContext } from './context';
import type { ApiClient } from './client';

export function useApi(): ApiClient {
  const ctx = useContext(ApiContext);
  if (!ctx) {
    throw new Error('useApi must be used inside <ApiProvider>');
  }
  return ctx;
}
