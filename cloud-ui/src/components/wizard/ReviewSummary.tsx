import { Card, makeStyles, tokens } from '@fluentui/react-components';
import { Fragment, type ReactNode } from 'react';

export interface ReviewRow {
  key: string;
  value: ReactNode;
  /** When true the row is omitted from the rendered grid entirely. */
  hidden?: boolean;
}

const useStyles = makeStyles({
  card: { padding: tokens.spacingHorizontalXXL },
  grid: {
    display: 'grid',
    gridTemplateColumns: '180px 1fr',
    rowGap: tokens.spacingVerticalS,
    columnGap: tokens.spacingHorizontalM,
  },
  key: { color: tokens.colorNeutralForeground3, fontSize: tokens.fontSizeBase200 },
  value: { fontSize: tokens.fontSizeBase200 },
});

export function ReviewSummary({ rows }: { rows: ReviewRow[] }) {
  const styles = useStyles();
  const visible = rows.filter((r) => !r.hidden);
  if (visible.length === 0) return null;
  return (
    <Card className={styles.card}>
      <div className={styles.grid}>
        {visible.map((row, i) => (
          <Fragment key={i}>
            <span className={styles.key}>{row.key}</span>
            <span className={styles.value}>{row.value}</span>
          </Fragment>
        ))}
      </div>
    </Card>
  );
}
