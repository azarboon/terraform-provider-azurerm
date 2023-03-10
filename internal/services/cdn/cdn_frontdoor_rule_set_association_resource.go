package cdn

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/hashicorp/terraform-provider-azurerm/internal/locks"
	"github.com/hashicorp/terraform-provider-azurerm/internal/services/cdn/parse"
	"github.com/hashicorp/terraform-provider-azurerm/internal/services/cdn/validate"
	"github.com/hashicorp/terraform-provider-azurerm/internal/tf/pluginsdk"
)

func resourceCdnFrontDoorRuleSetAssociation() *pluginsdk.Resource {
	return &pluginsdk.Resource{
		Create: resourceCdnFrontDoorRuleSetAssociationCreate,
		Read:   resourceCdnFrontDoorRuleSetAssociationRead,
		Update: resourceCdnFrontDoorRuleSetAssociationUpdate,
		Delete: resourceCdnFrontDoorRuleSetAssociationDelete,

		Timeouts: &pluginsdk.ResourceTimeout{
			Create: pluginsdk.DefaultTimeout(30 * time.Minute),
			Read:   pluginsdk.DefaultTimeout(5 * time.Minute),
			Update: pluginsdk.DefaultTimeout(30 * time.Minute),
			Delete: pluginsdk.DefaultTimeout(30 * time.Minute),
		},

		Importer: pluginsdk.ImporterValidatingResourceIdThen(func(id string) error {
			_, err := parse.FrontDoorRuleSetAssociationID(id)
			return err
		}, importCdnFrontDoorRuleSetAssociation()),

		Schema: map[string]*pluginsdk.Schema{
			"cdn_frontdoor_route_id": {
				Type:         pluginsdk.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validate.FrontDoorRouteID,
			},

			"cdn_frontdoor_rule_set_ids": {
				Type:     pluginsdk.TypeList,
				Required: true,

				Elem: &pluginsdk.Schema{
					Type:         pluginsdk.TypeString,
					ValidateFunc: validate.FrontDoorRuleSetID,
				},
			},
		},
	}
}

func resourceCdnFrontDoorRuleSetAssociationCreate(d *pluginsdk.ResourceData, meta interface{}) error {
	log.Printf("[INFO] preparing arguments for CDN FrontDoor Route <-> CDN FrontDoor Rule Set Association creation")
	routeId, err := parse.FrontDoorRouteID(d.Get("cdn_frontdoor_route_id").(string))
	if err != nil {
		return err
	}

	// NOTE: The association name is the name of the route the resource is being associated with...
	// e.g. subscriptions/12345678-1234-9876-4563-123456789012/resourceGroups/resGroup1/providers/Microsoft.Cdn/profiles/profile1/afdEndpoints/endpoint1/associations/route1
	id := parse.NewFrontDoorRuleSetAssociationID(routeId.SubscriptionId, routeId.ResourceGroup, routeId.ProfileName, routeId.AfdEndpointName, routeId.RouteName)

	// make sure the route and the rule sets exist and are associated with the route...
	if err := validateRuleSetsAssociation(d, meta, &id); err != nil {
		return fmt.Errorf("creating %s: %+v", id, err)
	}

	d.SetId(id.ID())

	return resourceCdnFrontDoorRuleSetAssociationRead(d, meta)
}

func resourceCdnFrontDoorRuleSetAssociationRead(d *pluginsdk.ResourceData, meta interface{}) error {
	id, err := parse.FrontDoorRuleSetAssociationID(d.Id())
	if err != nil {
		return err
	}

	routeRuleSetAssociations, _, err := getRouteRuleSetProperties(d, meta, id)
	if err != nil {
		return err
	}

	// NOTE: I am pulling the values directly from the route resource because if everything
	// worked correctly the state of this resource should match that state of the associated
	// route resource...
	d.Set("cdn_frontdoor_route_id", parse.NewFrontDoorRouteID(id.SubscriptionId, id.ResourceGroup, id.ProfileName, id.AfdEndpointName, id.AssociationName).ID())
	d.Set("cdn_frontdoor_rule_set_ids", routeRuleSetAssociations)

	return nil
}

func resourceCdnFrontDoorRuleSetAssociationUpdate(d *pluginsdk.ResourceData, meta interface{}) error {
	if d.HasChange("cdn_frontdoor_rule_set_ids") {
		id, err := parse.FrontDoorRuleSetAssociationID(d.Id())
		if err != nil {
			return err
		}

		// make sure the route and the rule sets exist and are associated with the route...
		if err := updateRuleSetsAssociations(d, meta, id, "updating", "waiting for the update of"); err != nil {
			// I think there is a bug here, if you update the route to remove the reference to the rule set it detects the change and throws the correct
			// error stating that the rule set is not associated with the route anymore, but the state file keeps the old value. When you update the
			// association resource to match the route resource (e.g. remove the reference from the rule set) the compare shows no changes. I think I
			// should remove the resource from the state file here and then throw the error, that way the state and azure will be in sync. That way the
			// next time you run plan it will show the association resource as a new resource and everything will be copasetic again. This is similar
			// to the existing behavior to in most resources read functions when the utils.ResponseWasNotFound returns true, it removes the resource from
			// state. I can't do it in the read because this resource does not exist in Azure, I have to do it when the update fails.
			d.SetId("")

			return err
		}

		return resourceCdnFrontDoorRuleSetAssociationRead(d, meta)
	}

	return nil
}

func resourceCdnFrontDoorRuleSetAssociationDelete(d *pluginsdk.ResourceData, meta interface{}) error {
	// since you are deleting the resource remove all rule set associations
	// on the route
	id, err := parse.FrontDoorRuleSetAssociationID(d.Id())
	if err != nil {
		return err
	}

	// Check to see if the route still exists and grab its properties...
	_, props, err := getRouteRuleSetProperties(d, meta, id)
	if err != nil {
		return err
	}

	// call set directly to skip all route and rule set validation...
	if err := setRouteRuleSetAssociations(d, meta, id, nil, props, "deleting", "waiting for the deletion of"); err != nil {
		return err
	}

	d.SetId("")

	return nil
}

func validateRuleSetsAssociation(d *pluginsdk.ResourceData, meta interface{}, id *parse.FrontDoorRuleSetAssociationId) error {
	newRuleSets := d.Get("cdn_frontdoor_rule_set_ids").([]interface{})

	if len(newRuleSets) == 0 || newRuleSets == nil {
		return nil
	}

	newID, _, err := expandRuleSets(newRuleSets)
	if err != nil {
		return err
	}

	// check for dupe entries in the resources rule set list...
	if err := ruleSetSliceHasDuplicates(newID); err != nil {
		return err
	}

	// Make sure the route exists and get the routes rule set association list...
	routeRuleSetAssociations, _, err := getRouteRuleSetProperties(d, meta, id)
	if err != nil {
		return err
	}

	// Make sure the rule sets exist...
	if err = ruleSetExists(d, meta, newID); err != nil {
		return err
	}

	// validate the new rule sets are associated with the route...
	if len(*newID) != 0 {
		notAssociated := make([]string, 0)

		for _, v := range *newID {
			if len(routeRuleSetAssociations) == 0 || !sliceContainsString(routeRuleSetAssociations, v.ID()) {
				notAssociated = append(notAssociated, v.RuleSetName)
			}
		}

		if len(notAssociated) != 0 {
			return fmt.Errorf("the CDN FrontDoor Route(Name: %q) is currently not associated with the CDN FrontDoor Rule Sets: %s. Please remove the CDN FrontDoor Rule Sets from your configuration", id.AssociationName, strings.Join(notAssociated, ", "))
		}
	}

	return nil
}

func updateRuleSetsAssociations(d *pluginsdk.ResourceData, meta interface{}, id *parse.FrontDoorRuleSetAssociationId, errorTxt string, futureErrorTxt string) error {
	newRuleSets := d.Get("cdn_frontdoor_rule_set_ids").([]interface{})

	// first validate the resource
	if err := validateRuleSetsAssociation(d, meta, id); err != nil {
		return err
	}

	// if the passed removeRuleSets is empty we need to remove all rule set associations from the route
	// I might have to hack the SDK to remove all of the rule set associations, since I believe the API
	// is expecting a null to remove all of the rule set associations with the route... :/

	// lock the route resource for update...
	locks.ByName(id.AssociationName, cdnFrontDoorRouteResourceName)
	defer locks.UnlockByName(id.AssociationName, cdnFrontDoorRouteResourceName)

	// Check to see if the route still exists and grab its properties...
	_, props, err := getRouteRuleSetProperties(d, meta, id)
	if err != nil {
		return err
	}

	// now set the new rule set associations on the route...
	if err := setRouteRuleSetAssociations(d, meta, id, newRuleSets, props, errorTxt, futureErrorTxt); err != nil {
		return err
	}

	return nil
}