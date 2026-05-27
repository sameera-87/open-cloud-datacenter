import type { ReactNode } from 'react';
import {
  Body1,
  Card,
  Spinner,
  Subtitle1,
  makeStyles,
  tokens,
} from '@fluentui/react-components';

const useStyles = makeStyles({
  loading: {
    padding: tokens.spacingHorizontalXXL,
    display: 'flex',
    justifyContent: 'center',
  },
  error: {
    padding: tokens.spacingHorizontalXXL,
    color: tokens.colorPaletteRedForeground1,
  },
  empty: {
    display: 'flex',
    flexDirection: 'column',
    alignItems: 'center',
    gap: tokens.spacingVerticalM,
    padding: tokens.spacingHorizontalXXXL,
    textAlign: 'center',
  },
  emptyIcon: {
    width: '56px',
    height: '56px',
    borderRadius: tokens.borderRadiusCircular,
    backgroundColor: tokens.colorBrandBackground2,
    color: tokens.colorBrandForeground1,
    display: 'grid',
    placeItems: 'center',
  },
  emptyDesc: {
    color: tokens.colorNeutralForeground3,
    maxWidth: '480px',
  },
});

export function LoadingState({ label }: { label: string }) {
  const styles = useStyles();
  return (
    <Card>
      <div className={styles.loading}>
        <Spinner label={label} />
      </div>
    </Card>
  );
}

export function ErrorState({ message }: { message: string }) {
  const styles = useStyles();
  return (
    <Card>
      <div className={styles.error}>{message}</div>
    </Card>
  );
}

interface EmptyStateProps {
  icon: ReactNode;
  title: string;
  description: ReactNode;
  action?: ReactNode;
  /** Optional muted hint rendered below the primary action — use for
   * secondary context (e.g. "You're the only member…") without promoting
   * it to a full CTA. */
  hint?: ReactNode;
}

export function EmptyState({ icon, title, description, action, hint }: EmptyStateProps) {
  const styles = useStyles();
  return (
    <Card>
      <div className={styles.empty}>
        <div className={styles.emptyIcon}>{icon}</div>
        <Subtitle1>{title}</Subtitle1>
        <Body1 className={styles.emptyDesc}>{description}</Body1>
        {action}
        {hint && <Body1 className={styles.emptyDesc}>{hint}</Body1>}
      </div>
    </Card>
  );
}
