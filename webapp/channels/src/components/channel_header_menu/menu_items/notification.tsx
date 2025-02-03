// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import React from 'react';
import {FormattedMessage} from 'react-intl';
import {useDispatch} from 'react-redux';

import {BellOutlineIcon} from '@mattermost/compass-icons/components';
import type {Channel} from '@mattermost/types/channels';
import type {UserProfile} from '@mattermost/types/users';

import {openModal} from 'actions/views/modals';

import ChannelNotificationsModal from 'components/channel_notifications_modal';
import * as Menu from 'components/menu';

import {ModalIdentifiers} from 'utils/constants';

type Props = {
    channel: Channel;
    user: UserProfile;
}

const Notification = ({channel, user}: Props): JSX.Element => {
    const dispatch = useDispatch();

    return (
        <Menu.Item
            leadingElement={<BellOutlineIcon size='18px'/>}
            id='channelNotificationPreferences'
            onClick={() => {
                dispatch(openModal({
                    modalId: ModalIdentifiers.CHANNEL_NOTIFICATIONS,
                    dialogType: ChannelNotificationsModal,
                    dialogProps: {
                        channel,
                        currentUser: user,
                    },
                }),
                );
            }}
            labels={
                <FormattedMessage
                    id='navbar.preferences'
                    defaultMessage='Notification Preferences'
                />
            }
        />
    );
};

export default React.memo(Notification);
