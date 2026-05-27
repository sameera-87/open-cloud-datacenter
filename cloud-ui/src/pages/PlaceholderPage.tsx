import { Body1, Subtitle1, Title2, makeStyles, tokens } from '@fluentui/react-components';

const useStyles = makeStyles({
  root: { padding: tokens.spacingHorizontalXXL, maxWidth: '1200px' },
  header: {
    marginBottom: tokens.spacingVerticalXL,
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalS,
  },
  subtitle: { color: tokens.colorNeutralForeground3 },
  body: {
    padding: tokens.spacingHorizontalXXL,
    border: `1px dashed ${tokens.colorNeutralStroke2}`,
    borderRadius: tokens.borderRadiusMedium,
    color: tokens.colorNeutralForeground3,
    backgroundColor: tokens.colorNeutralBackground2,
  },
});

interface PlaceholderPageProps {
  title: string;
  subtitle?: string;
  description?: string;
}

export default function PlaceholderPage({ title, subtitle, description }: PlaceholderPageProps) {
  const styles = useStyles();
  return (
    <div className={styles.root}>
      <div className={styles.header}>
        <Title2>{title}</Title2>
        {subtitle && <Subtitle1 className={styles.subtitle}>{subtitle}</Subtitle1>}
      </div>
      <div className={styles.body}>
        <Body1>{description ?? 'Coming in a later chunk.'}</Body1>
      </div>
    </div>
  );
}
