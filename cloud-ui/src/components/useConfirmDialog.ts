import { useContext } from 'react';
import { type ConfirmFn, ConfirmDialogContext } from './ConfirmDialogContext';

/** Returns a function that opens the app-root confirm dialog and resolves true/false. */
export function useConfirmDialog(): ConfirmFn {
  const fn = useContext(ConfirmDialogContext);
  if (!fn) {
    throw new Error('useConfirmDialog must be called inside <ConfirmDialogProvider>');
  }
  return fn;
}
