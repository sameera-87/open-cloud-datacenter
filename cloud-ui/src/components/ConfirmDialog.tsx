import {
  Button,
  Dialog,
  DialogActions,
  DialogBody,
  DialogContent,
  DialogSurface,
  DialogTitle,
  Field,
  Input,
  makeStyles,
  tokens,
} from '@fluentui/react-components';
import {
  type ReactNode,
  useCallback,
  useRef,
  useState,
} from 'react';
import {
  type ConfirmFn,
  type ConfirmOptions,
  ConfirmDialogContext,
} from './ConfirmDialogContext';

type Resolver = (ok: boolean) => void;

interface PendingDialog extends ConfirmOptions {
  resolve: Resolver;
}

const useStyles = makeStyles({
  dangerBtn: {
    backgroundColor: tokens.colorStatusDangerBackground3,
    color: tokens.colorNeutralForegroundOnBrand,
    ':hover': {
      backgroundColor: tokens.colorStatusDangerBackground3,
      opacity: 0.85,
    },
    ':active': {
      backgroundColor: tokens.colorStatusDangerBackground3,
      opacity: 0.7,
    },
  },
  typeField: {
    marginTop: tokens.spacingVerticalM,
  },
  typeLabel: {
    fontFamily: tokens.fontFamilyMonospace,
    fontWeight: tokens.fontWeightSemibold,
  },
});

/** Mount once at the app root. Provides the confirm dialog context to the entire tree. */
export function ConfirmDialogProvider({ children }: { children: ReactNode }) {
  const styles = useStyles();
  const [pending, setPending] = useState<PendingDialog | null>(null);
  const [typed, setTyped] = useState('');
  // Stable ref so close() always resolves the right promise even if pending is
  // already null in a stale closure.
  const resolveRef = useRef<Resolver | null>(null);

  const confirmDialog = useCallback<ConfirmFn>((opts) => {
    return new Promise<boolean>((resolve) => {
      resolveRef.current = resolve;
      setTyped('');
      setPending({ ...opts, resolve });
    });
  }, []);

  const close = (ok: boolean) => {
    resolveRef.current?.(ok);
    resolveRef.current = null;
    setPending(null);
    setTyped('');
  };

  const confirmLabel = pending?.confirmLabel ?? 'Confirm';
  const typeRequired = Boolean(pending?.typeToConfirm);
  const typeMatched = !typeRequired || typed === pending?.typeToConfirm;

  return (
    <ConfirmDialogContext.Provider value={confirmDialog}>
      {children}
      <Dialog
        open={pending !== null}
        onOpenChange={(_ev, data) => {
          if (!data.open) close(false);
        }}
      >
        <DialogSurface>
          <DialogBody>
            <DialogTitle>{pending?.title}</DialogTitle>
            <DialogContent>
              {pending?.body}
              {typeRequired && (
                <Field
                  className={styles.typeField}
                  label={
                    <>
                      Type <span className={styles.typeLabel}>{pending?.typeToConfirm}</span> to
                      confirm
                    </>
                  }
                >
                  <Input
                    value={typed}
                    onChange={(_ev, d) => setTyped(d.value)}
                    autoFocus
                  />
                </Field>
              )}
            </DialogContent>
            <DialogActions>
              <Button appearance="secondary" onClick={() => close(false)}>
                Cancel
              </Button>
              <Button
                appearance="primary"
                className={pending?.destructive ? styles.dangerBtn : undefined}
                disabled={!typeMatched}
                onClick={() => close(true)}
              >
                {confirmLabel}
              </Button>
            </DialogActions>
          </DialogBody>
        </DialogSurface>
      </Dialog>
    </ConfirmDialogContext.Provider>
  );
}
