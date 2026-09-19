export type WelcomeContributor = {
    name: string;
    avatar: string;
    signature: string;
    email?: string;
    wechat?: string;
    isFounder?: boolean;
};

// The public welcome page intentionally does not import README.md or contributor
// metadata. Keep this extension point product-owned so the customer bundle does
// not inherit repository authors, contacts, or upstream links.
export const welcomeContributors: WelcomeContributor[] = [];
