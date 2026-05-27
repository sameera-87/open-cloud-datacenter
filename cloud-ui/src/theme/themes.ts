import { createDarkTheme, createLightTheme } from '@fluentui/react-components';
import type { Theme } from '@fluentui/react-components';
import { wso2BrandVariants } from './brand';

export const wso2LightTheme: Theme = createLightTheme(wso2BrandVariants);
export const wso2DarkTheme: Theme = createDarkTheme(wso2BrandVariants);
