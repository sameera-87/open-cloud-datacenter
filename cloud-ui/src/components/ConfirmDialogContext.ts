import { createContext } from 'react';

export interface ConfirmOptions {
  title: string;
  body: string;
  confirmLabel?: string;
  /** When set, the user must type this exact string before the button enables. */
  typeToConfirm?: string;
  /** Renders the confirm button with danger colouring. */
  destructive?: boolean;
}

export type ConfirmFn = (opts: ConfirmOptions) => Promise<boolean>;

export const ConfirmDialogContext = createContext<ConfirmFn | null>(null);
